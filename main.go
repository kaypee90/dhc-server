package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	router := gin.Default()
	router.Use(Logger())
	router.Use(gin.Recovery())

	router.GET("/", func(c *gin.Context) {
		data := map[string]interface{}{
			"message": "Hello, JSON!",
		}

		c.JSON(http.StatusOK, data)
	})

	router.Run(":8080")
}
