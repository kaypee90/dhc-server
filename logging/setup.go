package logging

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Logger struct {
	logger *zap.Logger
}

type LogArg struct {
	Key   string
	Value interface{}
}

func (l *Logger) Info(msg string, args ...LogArg) {

	var fields []zapcore.Field
	for _, arg := range args {
		fields = append(fields, zap.Any(arg.Key, arg.Value))
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
