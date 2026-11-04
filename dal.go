package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrSeriesLimit is returned when a report would create a new
// (source, check) series after the configured limit was reached.
var ErrSeriesLimit = errors.New("series limit reached")

// Metric is one health check report as stored in the database.
type Metric struct {
	gorm.Model
	Label       string `json:"label" gorm:"index:idx_metrics_series,priority:2"`
	Value       int    `json:"value"`
	Description string `json:"description"`
	Source      string `json:"source" gorm:"index:idx_metrics_series,priority:1"`
}

// MetricInput is the request body for POST /v1/metrics. It is kept apart from
// the database model so clients cannot set ID or timestamps. Value is a
// pointer so that 0 counts as present: a plain int with binding:"required"
// rejects 0, which is a valid health check value.
type MetricInput struct {
	Label       string `json:"label" binding:"required,max=128,safetext"`
	Value       *int   `json:"value" binding:"required"`
	Description string `json:"description" binding:"required,max=1024,safetext"`
	Source      string `json:"source" binding:"required,max=128,safetext"`
}

// SeriesStatus is the latest report for one (source, check) pair.
type SeriesStatus struct {
	Source     string    `json:"source"`
	Label      string    `json:"label"`
	Value      int       `json:"value"`
	ReportedAt time.Time `json:"reported_at"`
}

type MetricSummary struct {
	Label string `json:"label"`
	Value int    `json:"value"`
	Count int    `json:"count"`
}

// Store wraps the database.
type Store struct {
	db *gorm.DB
}

// OpenStore opens (or creates) the SQLite database at path and migrates the
// schema. It uses a pure-Go SQLite driver so the server builds without cgo.
func OpenStore(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		// WAL lets reads run during writes; busy_timeout waits for locks
		// instead of failing straight away.
		dsn = path + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		// Store timestamps in UTC so they compare consistently even if the
		// host's timezone changes (e.g. a database moved into a container).
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time. A single connection avoids
	// "database is locked" errors and keeps an in-memory database shared.
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(&Metric{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// CreateMetric stores a report. It returns ErrSeriesLimit if the report
// belongs to a new series and maxSeries series already exist.
func (s *Store) CreateMetric(ctx context.Context, m *Metric, maxSeries int) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&Metric{}).
			Where("source = ? AND label = ?", m.Source, m.Label).
			Limit(1).Count(&existing).Error; err != nil {
			return err
		}
		if existing == 0 {
			// GORM drops DISTINCT from Count, so count groups explicitly.
			var series int64
			if err := tx.Raw(`SELECT COUNT(*) FROM (
				SELECT 1 FROM metrics WHERE deleted_at IS NULL GROUP BY source, label
			)`).Scan(&series).Error; err != nil {
				return err
			}
			if series >= int64(maxSeries) {
				return ErrSeriesLimit
			}
		}
		return tx.Create(m).Error
	})
}

// ListMetrics returns up to limit reports, newest first.
func (s *Store) ListMetrics(ctx context.Context, limit int) ([]Metric, error) {
	var metrics []Metric
	err := s.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&metrics).Error
	return metrics, err
}

// LatestPerSeries returns the most recent report for each (source, check)
// pair, up to limit pairs.
func (s *Store) LatestPerSeries(ctx context.Context, limit int) ([]SeriesStatus, error) {
	var rows []SeriesStatus
	err := s.db.WithContext(ctx).Raw(`
		SELECT m.source, m.label, m.value, m.created_at AS reported_at
		FROM metrics m
		JOIN (
			SELECT MAX(id) AS id FROM metrics
			WHERE deleted_at IS NULL
			GROUP BY source, label
		) latest ON latest.id = m.id
		ORDER BY m.source, m.label
		LIMIT ?`, limit).Scan(&rows).Error
	return rows, err
}

// Summary counts reports for each (label, value) pair.
func (s *Store) Summary(ctx context.Context) ([]MetricSummary, error) {
	result := []MetricSummary{}
	err := s.db.WithContext(ctx).Model(&Metric{}).
		Select("label, value, count(*) AS count").
		Group("label, value").
		Order("label, value").
		Scan(&result).Error
	return result, err
}

// PurgeOlderThan permanently deletes reports created before cutoff.
//
// Timestamps are stored as text, and older rows (written by the previous
// cgo driver) use a different layout and local timezone offsets. Comparing
// with julianday() parses both layouts and their offsets, so the comparison
// is by actual time rather than by string.
func (s *Store) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Unscoped().
		Where("julianday(created_at) < julianday(?)", cutoff.UTC().Format(time.RFC3339Nano)).
		Delete(&Metric{})
	return res.RowsAffected, res.Error
}
