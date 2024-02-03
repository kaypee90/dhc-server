package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	HTTP_GET  = "GET"
	HTTP_POST = "POST"
)

func TestPingRoute(t *testing.T) {
	router := setupRouter()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Healthy")
}

func TestCreateMetric(t *testing.T) {
	router := setupRouter()

	payload := map[string]interface{}{
		"label":       "db-check",
		"value":       1,
		"description": "failing",
		"source":      "dhc-service",
	}

	jsonPayload, err := json.Marshal(payload)
	assert.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(HTTP_POST, "/v1/metrics", bytes.NewBuffer(jsonPayload))

	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	metric := Metric{}
	context.Database.First(&metric)

	assert.Equal(t, metric.Label, "db-check")
	assert.Equal(t, metric.Value, 1)
}

func TestGetMetrics(t *testing.T) {
	metric := Metric{
		Label:       "celery-check",
		Value:       1,
		Description: "failing",
		Source:      "celery-check-source",
	}
	context.Database.Create(&metric)

	router := setupRouter()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(HTTP_GET, "/v1/metrics", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	expected := "instrumentation_library_metrics"
	if !strings.Contains(w.Body.String(), expected) {
		t.Errorf("Handler returned unexpected body: got %v want %v",
			w.Body.String(), expected)
	}

	expectedName := "\"name\":\"celery-check\""
	if !strings.Contains(w.Body.String(), expectedName) {
		t.Errorf("Handler returned unexpected body: got %v want %v",
			w.Body.String(), expectedName)
	}

	expectedSource := "\"source\":\"celery-check-source\""
	if !strings.Contains(w.Body.String(), expectedSource) {
		t.Errorf("Handler returned unexpected body: got %v want %v",
			w.Body.String(), expectedSource)
	}
}

func TestGetMetricsSummary(t *testing.T) {
	metric := Metric{
		Label:       "celery-check",
		Value:       1,
		Description: "failing",
		Source:      "celery-check-source",
	}
	context.Database.Create(&metric)

	router := setupRouter()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(HTTP_GET, "/v1/metrics/summary", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	expected := "\"Label\":\"celery-check\",\"Value\":1,\"Count\":"
	if !strings.Contains(w.Body.String(), expected) {
		t.Errorf("Handler returned unexpected body: got %v want %v",
			w.Body.String(), expected)
	}
}
