package main

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

var context = DatabaseContext{DatabaseName: "dhc.db"}

func setupRouter() *gin.Engine {
	logger, err := NewLogger()
	if err != nil {
		panic(err)
	}

	router := gin.Default()
	router.Use(cors.Default())
	router.Use(SetupLogger(logger))
	router.Use(gin.Recovery())
	context.initDB()

	v1Handler := VersionOneHandler{}
	v1Handler.ConfigureLogger(logger)

	v1 := router.Group("/v1")
	{
		v1.GET("/", v1Handler.HealthCheck)
		v1.POST("/metrics", v1Handler.CreateMetric)
		v1.GET("/metrics", v1Handler.GetMetrics)
		v1.GET("/metrics/summary", v1Handler.GetMetricsSummary)
	}
	return router
}

func main() {
	router := setupRouter()
	router.Run(":8080")
}
