//nolint:testpackage // white-box test requires internal access
package log

import (
	"bytes"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var errTest = errors.New("test error")

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	hook := &logrusZapHook{logger: lg}
	entry := logrus.NewEntry(logrus.New())
	entry.Level = logrus.InfoLevel
	entry.Message = "hello"
	entry.Time = time.Now()
	entry.Data = logrus.Fields{
		"component": "test",
	}

	err := hook.Fire(entry)
	require.NoError(t, err, "hook fire failed")

	out := buf.String()
	assert.Contains(t, out, `"msg":"hello"`, "expected message in output")
	assert.Contains(t, out, `"component":"test"`, "expected fields in output")
}

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook_NilLogger(t *testing.T) {
	hook := &logrusZapHook{logger: nil}
	entry := logrus.NewEntry(logrus.New())
	entry.Level = logrus.InfoLevel
	entry.Message = "test"
	entry.Time = time.Now()

	err := hook.Fire(entry)
	require.NoError(t, err, "Fire with nil logger should not error")
}

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook_WithErrorField(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	hook := &logrusZapHook{logger: lg}
	entry := logrus.NewEntry(logrus.New())
	entry.Level = logrus.ErrorLevel
	entry.Message = "operation failed"
	entry.Time = time.Now()
	entry.Data = logrus.Fields{
		"error": errTest,
	}

	err := hook.Fire(entry)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"operation failed"`)
	assert.Contains(t, out, "test error")
}

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook_WithCaller(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	hook := &logrusZapHook{logger: lg}

	// Create logger with caller reporting enabled.
	logrusLogger := logrus.New()
	logrusLogger.SetReportCaller(true)

	entry := logrus.NewEntry(logrusLogger)
	entry.Level = logrus.InfoLevel
	entry.Message = "with caller"
	entry.Time = time.Now()
	entry.Caller = &runtime.Frame{
		Function: "TestFunction",
		File:     "test_file.go",
		Line:     42,
	}

	err := hook.Fire(entry)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"with caller"`)
	assert.Contains(t, out, `"logrus.caller":"TestFunction"`)
	assert.Contains(t, out, `"logrus.file":"test_file.go"`)
	assert.Contains(t, out, `"logrus.line":42`)
}

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook_Levels(t *testing.T) {
	hook := &logrusZapHook{logger: zap.NewNop()}
	levels := hook.Levels()

	assert.Equal(t, logrus.AllLevels, levels, "hook should return all logrus levels")
}

//nolint:paralleltest // Uses shared global logger state.
func TestMapLogrusLevel(t *testing.T) {
	tests := []struct {
		name        string
		logrusLevel logrus.Level
		expectedZap zapcore.Level
	}{
		{"trace to debug", logrus.TraceLevel, zapcore.DebugLevel},
		{"debug to debug", logrus.DebugLevel, zapcore.DebugLevel},
		{"info to info", logrus.InfoLevel, zapcore.InfoLevel},
		{"warn to warn", logrus.WarnLevel, zapcore.WarnLevel},
		{"error to error", logrus.ErrorLevel, zapcore.ErrorLevel},
		{"fatal to error", logrus.FatalLevel, zapcore.ErrorLevel},
		{"panic to error", logrus.PanicLevel, zapcore.ErrorLevel},
	}

	for _, tt := range tests { //nolint:paralleltest // Serial subtests avoid shared logger races.
		t.Run(tt.name, func(t *testing.T) {
			result := mapLogrusLevel(tt.logrusLevel)
			assert.Equal(t, tt.expectedZap, result)
		})
	}
}

//nolint:paralleltest // Uses shared global logger state.
func TestLogrusZapHook_AllLogLevels(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	hook := &logrusZapHook{logger: lg}

	logLevels := []logrus.Level{
		logrus.TraceLevel,
		logrus.DebugLevel,
		logrus.InfoLevel,
		logrus.WarnLevel,
		logrus.ErrorLevel,
	}

	for _, level := range logLevels {
		entry := logrus.NewEntry(logrus.New())
		entry.Level = level
		entry.Message = "test message for " + level.String()
		entry.Time = time.Now()

		err := hook.Fire(entry)
		require.NoError(t, err, "Fire should not error for level %s", level)
	}

	out := buf.String()
	assert.Contains(t, out, "test message for")
}

//nolint:paralleltest // Uses shared global logger state.
func TestMapLogrusLevel_DefaultCase(t *testing.T) {
	// Test with an undefined level value (simulating future extension or invalid level)
	// Cast a value outside the defined logrus levels
	undefinedLevel := logrus.Level(99)
	result := mapLogrusLevel(undefinedLevel)
	assert.Equal(t, zapcore.InfoLevel, result, "undefined level should default to info")
}

//nolint:paralleltest // Uses shared global logger state and sync.Once.
func TestInstallLogrusBridge(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	// Install the bridge
	InstallLogrusBridge(lg)

	// Log via logrus
	logrus.Info("test message from logrus")
	_ = lg.Sync()

	// Verify the message was forwarded to zap
	out := buf.String()
	assert.Contains(t, out, "test message from logrus")
}

//nolint:paralleltest // Uses shared global logger state and sync.Once.
func TestInstallLogrusBridge_NilLogger(_ *testing.T) {
	// Should not panic with nil logger
	InstallLogrusBridge(nil)
	// Success if no panic occurred
}
