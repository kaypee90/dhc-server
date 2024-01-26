package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
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
	req, _ := http.NewRequest("POST", "/v1/metrics", bytes.NewBuffer(jsonPayload))
	assert.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	metric := Metric{}
	context.Database.First(&metric)

	assert.Equal(t, metric.Label, "db-check")
	assert.Equal(t, metric.Value, 1)
}
