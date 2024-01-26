package main

import (
	"github.com/gin-gonic/gin"
)

var context = DatabaseContext{DatabaseName: "dhc.db"}

func setupRouter() *gin.Engine {
	router := gin.Default()
	router.Use(Logger())
	router.Use(gin.Recovery())
	context.initDB()

	v1Handler := VersionOneHandler{}

	v1 := router.Group("/v1")
	{
		v1.GET("/", v1Handler.HealthCheck)
		v1.POST("/metrics", v1Handler.CreateMetric)
	}
	return router
}

func main() {
	router := setupRouter()
	router.Run(":8080")
}
