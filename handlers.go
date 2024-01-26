package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type VersionOneHandler struct {
}

func (h *VersionOneHandler) HealthCheck(c *gin.Context) {
	data := map[string]interface{}{
		"message": "Healthy",
	}

	c.JSON(http.StatusOK, data)
}

func (h *VersionOneHandler) CreateMetric(c *gin.Context) {
	var newMetric Metric

	// Bind the request body to the Metric struct
	if err := c.ShouldBindJSON(&newMetric); err != nil {
		log.Println(err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data := context.createNewMetric(newMetric)

	c.JSON(http.StatusCreated, data)
}
