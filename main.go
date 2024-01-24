package main

import (
	"github.com/gin-gonic/gin"
)

func setupRouter() *gin.Engine {
	router := gin.Default()
	router.Use(Logger())
	router.Use(gin.Recovery())

	v1Handler := VersionOneHandler{}

	v1 := router.Group("/v1")
	{
		v1.GET("/", v1Handler.HealhCheck)
	}
	return router
}

func main() {
	router := setupRouter()
	router.Run(":8080")
}
