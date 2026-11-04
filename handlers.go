package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	l "dhc-server/logging"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

const (
	defaultListLimit = 1000
	maxListLimit     = 10000
)

// Response types for the JSON API under /v1.

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

type GetMetricsResponse struct {
	Metrics []Instrument `json:"metrics"`
}

type MetricSummaryResponse struct {
	Data []MetricSummary `json:"data"`
}

type LatestResponse struct {
	Data []SeriesStatus `json:"data"`
}

type VersionOneHandler struct {
	store     *Store
	telemetry *Telemetry
	logger    *l.Logger
	maxSeries int
}

func (h *VersionOneHandler) HealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		h.logger.Error("database ping failed", l.LogArg{Key: "error", Value: err.Error()})
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Unhealthy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Healthy"})
}

func (h *VersionOneHandler) CreateMetric(c *gin.Context) {
	var input MetricInput
	if err := c.ShouldBindJSON(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": validationMessage(err)})
		return
	}

	metric := Metric{
		Label:       input.Label,
		Value:       *input.Value,
		Description: input.Description,
		Source:      input.Source,
	}
	if err := h.store.CreateMetric(c.Request.Context(), &metric, h.maxSeries); err != nil {
		if errors.Is(err, ErrSeriesLimit) {
			h.logger.Warn("rejected report: series limit reached",
				l.LogArg{Key: "source", Value: metric.Source},
				l.LogArg{Key: "check", Value: metric.Label})
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "too many distinct source/label pairs; raise DHC_MAX_SERIES or reuse existing ones"})
			return
		}
		h.internalError(c, "store metric", err)
		return
	}

	h.telemetry.RecordReport(c.Request.Context())
	c.JSON(http.StatusCreated, metric)
}

// GetMetrics returns stored reports grouped by source. It takes an optional
// ?limit= (default 1000, max 10000), newest reports first.
func (h *VersionOneHandler) GetMetrics(c *gin.Context) {
	limit, ok := parseLimit(c)
	if !ok {
		return
	}
	dbMetrics, err := h.store.ListMetrics(c.Request.Context(), limit)
	if err != nil {
		h.internalError(c, "list metrics", err)
		return
	}

	groupedMetrics := make(map[string][]Metric)
	for _, m := range dbMetrics {
		groupedMetrics[m.Source] = append(groupedMetrics[m.Source], m)
	}

	sources := make([]string, 0, len(groupedMetrics))
	for source := range groupedMetrics {
		sources = append(sources, source)
	}
	// Map order is random in Go; sort so responses are stable.
	sort.Strings(sources)

	libraryMetrics := []InstrumentationLibrary{}
	for _, source := range sources {
		library := InstrumentationLibrary{Name: source, Metrics: []InstrumentationMetric{}}
		for _, item := range groupedMetrics[source] {
			library.Metrics = append(library.Metrics, InstrumentationMetric{
				Timestamp: item.CreatedAt.UTC().Format(time.RFC3339Nano),
				DataPoints: []DataPoint{{
					"labels": map[string]interface{}{
						"name":        item.Label,
						"source":      item.Source,
						"description": item.Description,
					},
					"value": map[string]interface{}{
						"int_value": item.Value,
					},
				}},
			})
		}
		libraryMetrics = append(libraryMetrics, library)
	}

	c.JSON(http.StatusOK, GetMetricsResponse{Metrics: []Instrument{{
		Name:           "health_checks_results",
		Description:    "Health checks results",
		Unit:           "1",
		LibraryMetrics: libraryMetrics,
	}}})
}

// GetLatest returns the latest report for each source/label pair.
func (h *VersionOneHandler) GetLatest(c *gin.Context) {
	rows, err := h.store.LatestPerSeries(c.Request.Context(), h.maxSeries)
	if err != nil {
		h.internalError(c, "read latest metrics", err)
		return
	}
	if rows == nil {
		rows = []SeriesStatus{}
	}
	c.JSON(http.StatusOK, LatestResponse{Data: rows})
}

func (h *VersionOneHandler) GetMetricsSummary(c *gin.Context) {
	data, err := h.store.Summary(c.Request.Context())
	if err != nil {
		h.internalError(c, "summarise metrics", err)
		return
	}
	c.JSON(http.StatusOK, MetricSummaryResponse{Data: data})
}

// internalError logs the real error and returns a generic message so that
// database details never reach the client.
func (h *VersionOneHandler) internalError(c *gin.Context, op string, err error) {
	h.logger.Error(op+" failed", l.LogArg{Key: "error", Value: err.Error()})
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

func parseLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return defaultListLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxListLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be an integer between 1 and " + strconv.Itoa(maxListLimit)})
		return 0, false
	}
	return n, true
}

// validationMessage turns binding errors into short messages that name the
// field and rule without echoing the submitted value.
func validationMessage(err error) string {
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return "invalid JSON body"
	}
	msg := ""
	for i, fe := range verrs {
		if i > 0 {
			msg += "; "
		}
		field := jsonFieldName(fe.Field())
		switch fe.Tag() {
		case "required":
			msg += field + " is required"
		case "max":
			msg += field + " must be at most " + fe.Param() + " characters"
		case "safetext":
			msg += field + " must be non-blank text without control characters"
		default:
			msg += field + " is invalid"
		}
	}
	return msg
}

func jsonFieldName(goName string) string {
	switch goName {
	case "Label":
		return "label"
	case "Value":
		return "value"
	case "Description":
		return "description"
	case "Source":
		return "source"
	}
	return goName
}
