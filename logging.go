package main

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Logger struct {
	logger *zap.Logger
}

type LogArg struct {
	key   string
	value interface{}
}

func (l Logger) Info(msg string, args ...LogArg) {

	var fields []zapcore.Field
	for _, arg := range args {
		fields = append(fields, zap.Any(arg.key, arg.value))
	}

	l.logger.Info(msg, fields...)
}

func NewLogger() (*Logger, error) {
	// Initialize zap logger
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, err
	}
	return &Logger{logger: logger}, nil
}

func SetupLogger(logger *Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		t := time.Now()

		c.Next()
		latency := time.Since(t)

		status := c.Writer.Status()

		logger.Info("response",
			LogArg{key: "latency", value: latency},
			LogArg{key: "status_code", value: status},
		)
	}
}
