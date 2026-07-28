package log

import (
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	loggerMu sync.RWMutex
	logger   *zap.Logger
)

//nolint:gochecknoinits // Package initialization for global logger is required at startup.
func init() {
	lg, err := DefaultZapConfig().Build()
	if err != nil {
		panic(err)
	}

	SetZap(lg)
}

// SetZap sets the global logger instance and replaces the zap globals.
func SetZap(lg *zap.Logger) {
	loggerMu.Lock()
	defer loggerMu.Unlock()

	logger = lg
	zap.ReplaceGlobals(lg)
}

// L returns the current global logger instance.
func L() *zap.Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()

	return logger
}

// S returns a sugar logger wrapping the current global logger.
func S() *Sugar {
	loggerMu.RLock()
	defer loggerMu.RUnlock()

	return &Sugar{
		SugaredLogger: logger.Sugar(),
	}
}

// Sugar wraps zap.SugaredLogger to provide a convenient logging interface.
type Sugar struct {
	*zap.SugaredLogger
}

// Printf implements "tailscale.com/types/logger".Logf.
func (l *Sugar) Printf(format string, v ...any) {
	l.Infof(format, v...)
}

// DefaultZapConfig returns a new default zap logger configuration.
func DefaultZapConfig() zap.Config {
	return zap.Config{
		Level: zap.NewAtomicLevelAt(zap.InfoLevel),

		Development: false,
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},

		Encoding: "json",

		// copied from "zap.NewProductionEncoderConfig" with some updates
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},

		// Use "/dev/null" to discard all
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
}

// ParseLogLevel parses a log level string into a zapcore.Level.
// Supports "debug", "info", "warn", "error". Defaults to InfoLevel.
func ParseLogLevel(raw string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
