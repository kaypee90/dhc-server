package main

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Metric struct {
	gorm.Model
	Label       string `json:"label" form:"label" binding:"required"`
	Value       int    `json:"value" form:"value" binding:"required"`
	Description string `json:"description" form:"description" binding:"required"`
	Source      string `json:"source" form:"source" binding:"required"`
}

type DatabaseContext struct {
	DatabaseName string
	Database     *gorm.DB
}

type DataPoint map[string]interface{}

type InstrumentationMetric struct {
	Timestamp  string      `json:"timestamp"`
	DataPoints []DataPoint `json:"data_points"`
}

type InstrumentationLibrary struct {
	Name    string                  `json:"instrumentation_library"`
	Metrics []InstrumentationMetric `json:"metrics"`
}

type Instrument struct {
	Name           string                   `json:"name"`
	Description    string                   `json:"description"`
	Unit           string                   `json:"unit"`
	LibraryMetrics []InstrumentationLibrary `json:"instrumentation_library_metrics"`
}

type MetricSummary struct {
	Label string `json:"label"`
	Value int    `json:"value"`
	Count int    `json:"count"`
}

type MetricSummaryResponse struct {
	Data []MetricSummary `json:"data"`
}

type GetMetricsResponse struct {
	Metrics []Instrument `json:"metrics"`
}

func (context *DatabaseContext) initDB() {
	db, err := gorm.Open(sqlite.Open(context.DatabaseName), &gorm.Config{})

	if err != nil {
		panic("Failed to connect to the database")
	}

	// Migrate the schema
	db.AutoMigrate(&Metric{})

	context.Database = db
}

func (context *DatabaseContext) createNewMetric(metric Metric) Metric {
	// Create the metric in the database
	context.Database.Create(&metric)

	return metric
}

func (context *DatabaseContext) getAllMetrics() []Metric {
	var metrics []Metric
	context.Database.Find(&metrics)
	return metrics
}

func (context *DatabaseContext) getMetricsSummary() MetricSummaryResponse {
	var result []MetricSummary

	context.Database.Table("metrics").
		Select("Label, Value, count(*) as count").
		Group("Label, Value").
		Scan(&result)

	return MetricSummaryResponse{Data: result}
}
