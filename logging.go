package main

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		t := time.Now()

		c.Next()
		latency := time.Since(t)

		status := c.Writer.Status()
		log.Printf("{\"latency\": %s, \"reponse_status_code\": %d} ", latency, status)
	}
}
