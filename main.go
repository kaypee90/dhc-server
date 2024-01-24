package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	router := gin.Default()

	router.GET("/", func(c *gin.Context) {
		data := map[string]interface{}{
			"message": "Hello, JSON!",
		}

		// Use the JSON method to send the map as a JSON response
		c.JSON(http.StatusOK, data)
	})

	router.Run(":8080")
}
