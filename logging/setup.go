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

func fields(args []LogArg) []zapcore.Field {
	out := make([]zapcore.Field, 0, len(args))
	for _, arg := range args {
		out = append(out, zap.Any(arg.Key, arg.Value))
	}
	return out
}

func (l *Logger) Info(msg string, args ...LogArg) {
	l.logger.Info(msg, fields(args)...)
}

func (l *Logger) Warn(msg string, args ...LogArg) {
	l.logger.Warn(msg, fields(args)...)
}

func (l *Logger) Error(msg string, args ...LogArg) {
	l.logger.Error(msg, fields(args)...)
}

// Sync flushes buffered log entries. Call it before the process exits.
func (l *Logger) Sync() error {
	return l.logger.Sync()
}

// NewLogger returns a JSON logger suited to production.
func NewLogger() (*Logger, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, err
	}
	return &Logger{logger: logger}, nil
}

// NewNop returns a logger that discards everything, for tests.
func NewNop() *Logger {
	return &Logger{logger: zap.NewNop()}
}
