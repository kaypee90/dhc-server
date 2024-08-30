package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type VersionOneHandler struct {
	logger *Logger
}

func (h *VersionOneHandler) ConfigureLogger(logger *Logger) {
	// Set the logger
	h.logger = logger
}

func (h *VersionOneHandler) HealthCheck(c *gin.Context) {
	data := map[string]string{
		"message": "Healthy",
	}

	c.JSON(http.StatusOK, data)
}

func (h *VersionOneHandler) CreateMetric(c *gin.Context) {
	var newMetric Metric

	// Bind the request body to the Metric struct
	if err := c.ShouldBindJSON(&newMetric); err != nil {
		h.logger.Info(err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data := context.createNewMetric(newMetric)

	c.JSON(http.StatusCreated, data)
}

func (h *VersionOneHandler) GetMetrics(c *gin.Context) {
	dbMetrics := context.getAllMetrics()

	groupedMetrics := make(map[string][]Metric)
	var libraryMetrics []InstrumentationLibrary

	for _, dbMetric := range dbMetrics {
		if _, exists := groupedMetrics[dbMetric.Source]; exists {
			groupedMetrics[dbMetric.Source] = append(groupedMetrics[dbMetric.Source], dbMetric)
		} else {
			h.logger.Info("Key doesn't exist in the grouped metrics, adding it", LogArg{key: "source", value: dbMetric.Source})
			groupedMetrics[dbMetric.Source] = []Metric{dbMetric}
		}
	}

	for key, value := range groupedMetrics {
		h.logger.Info("Process grouped metric", LogArg{key: "source", value: key})
		var metrics []InstrumentationMetric
		libraryMetric := InstrumentationLibrary{
			Name:    key,
			Metrics: metrics,
		}

		for _, item := range value {
			datapoints := []DataPoint{
				map[string]interface{}{
					"labels": map[string]interface{}{
						"name":        item.Label,
						"source":      item.Source,
						"description": item.Description,
					},
					"value": map[string]interface{}{
						"int_value": item.Value,
					},
				},
			}

			libraryMetric.Metrics = append(libraryMetric.Metrics, InstrumentationMetric{
				Timestamp:  item.CreatedAt.String(),
				DataPoints: datapoints,
			})
		}

		h.logger.Info("Appending library metric", LogArg{key: "source", value: libraryMetric.Name})
		libraryMetrics = append(libraryMetrics, libraryMetric)
	}

	instruments := []Instrument{
		{
			Name:           "heath_checks_results",
			Description:    "Heath checks results",
			Unit:           "1",
			LibraryMetrics: libraryMetrics,
		},
	}

	data := GetMetricsResponse{
		Metrics: instruments,
	}

	c.JSON(http.StatusOK, data)
}

func (h *VersionOneHandler) GetMetricsSummary(c *gin.Context) {
	data := context.getMetricsSummary()
	c.JSON(http.StatusOK, data)
}
