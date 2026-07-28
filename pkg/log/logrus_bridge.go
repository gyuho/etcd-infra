package log

import (
	"fmt"
	"io"
	"sync"

	"github.com/sirupsen/logrus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logrusBridgeOnce sync.Once

// InstallLogrusBridge forwards logrus entries to the provided zap logger.
func InstallLogrusBridge(lg *zap.Logger) {
	if lg == nil {
		return
	}

	logrusBridgeOnce.Do(func() {
		logrus.SetOutput(io.Discard)
		logrus.AddHook(&logrusZapHook{logger: lg})
	})
}

type logrusZapHook struct {
	logger *zap.Logger
}

func (h *logrusZapHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *logrusZapHook) Fire(entry *logrus.Entry) error {
	if h.logger == nil {
		return nil
	}

	fields := make([]zap.Field, 0, len(entry.Data))
	for key, value := range entry.Data {
		if err, ok := value.(error); ok {
			fields = append(fields, zap.NamedError(key, err))
		} else {
			fields = append(fields, zap.Any(key, value))
		}
	}
	if entry.Caller != nil {
		fields = append(fields,
			zap.String("logrus.caller", entry.Caller.Function),
			zap.String("logrus.file", entry.Caller.File),
			zap.Int("logrus.line", entry.Caller.Line),
		)
	}

	zapEntry := zapcore.Entry{
		Time:    entry.Time,
		Level:   mapLogrusLevel(entry.Level),
		Message: entry.Message,
	}

	err := h.logger.Core().Write(zapEntry, fields)
	if err != nil {
		return fmt.Errorf("write log: %w", err)
	}

	return nil
}

func mapLogrusLevel(level logrus.Level) zapcore.Level {
	switch level {
	case logrus.TraceLevel, logrus.DebugLevel:
		return zapcore.DebugLevel
	case logrus.InfoLevel:
		return zapcore.InfoLevel
	case logrus.WarnLevel:
		return zapcore.WarnLevel
	case logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel:
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
