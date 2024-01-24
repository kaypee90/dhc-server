package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type VersionOneHandler struct {
}

func (h *VersionOneHandler) HealhCheck(c *gin.Context) {
	data := map[string]interface{}{
		"message": "Healthy",
	}

	c.JSON(http.StatusOK, data)
}
