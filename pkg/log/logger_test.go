//nolint:paralleltest // Uses shared global logger state.
package log

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestSetZap(t *testing.T) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	lg, err := cfg.Build()
	require.NoError(t, err, "Failed to build logger")

	SetZap(lg)

	assert.Equal(t, lg, L(), "expected logger to be set")

	L().Info("done")
}

func TestL(t *testing.T) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	lg, err := cfg.Build()
	require.NoError(t, err, "Failed to build logger")

	SetZap(lg)

	retrievedLogger := L()
	assert.Equal(t, lg, retrievedLogger, "expected logger to be set")

	S().Infow("done")
}

func TestS(t *testing.T) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	lg, err := cfg.Build()
	require.NoError(t, err)

	SetZap(lg)

	sugar := S()
	assert.NotNil(t, sugar, "expected sugar logger to be set")
	assert.NotNil(t, sugar.SugaredLogger, "expected sugared logger to be set")

	// Test basic logging methods
	sugar.Infow("test message", "key", "value")
	sugar.Debugw("debug message", "count", 42)
}

func TestSugar_Printf(t *testing.T) {
	// Create a buffer to capture log output
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	SetZap(lg)

	sugar := S()
	sugar.Printf("formatted message: %s %d", "test", 123)

	// Sync to ensure output is written
	_ = lg.Sync()

	// Verify the output contains the formatted message
	output := buf.String()
	assert.Contains(t, output, "formatted message: test 123")
}

func TestDefaultZapConfig(t *testing.T) {
	cfg := DefaultZapConfig()

	// Verify default configuration values
	assert.Equal(t, zap.InfoLevel, cfg.Level.Level(), "expected info level")
	assert.False(t, cfg.Development, "expected development to be false")
	assert.Equal(t, "json", cfg.Encoding, "expected json encoding")

	// Verify encoder config
	assert.Equal(t, "ts", cfg.EncoderConfig.TimeKey)
	assert.Equal(t, "level", cfg.EncoderConfig.LevelKey)
	assert.Equal(t, "msg", cfg.EncoderConfig.MessageKey)
	assert.Equal(t, "caller", cfg.EncoderConfig.CallerKey)

	// Verify sampling config
	assert.NotNil(t, cfg.Sampling)
	assert.Equal(t, 100, cfg.Sampling.Initial)
	assert.Equal(t, 100, cfg.Sampling.Thereafter)

	// Verify output paths
	assert.Contains(t, cfg.OutputPaths, "stderr")
	assert.Contains(t, cfg.ErrorOutputPaths, "stderr")

	// Verify we can build a logger from this config
	lg, err := cfg.Build()
	require.NoError(t, err)
	assert.NotNil(t, lg)
}

func TestLoggerConcurrency(t *testing.T) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	lg, err := cfg.Build()
	require.NoError(t, err)

	done := make(chan bool)

	// Concurrent writers
	for i := range 10 {
		go func(id int) {
			for j := range 100 {
				SetZap(lg)
				L().Info("concurrent log", zap.Int("id", id), zap.Int("iter", j))
				S().Infow("concurrent sugar log", "id", id, "iter", j)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}
}

func TestDefaultLoggerInitialization(t *testing.T) {
	// The package init() should have set up a default logger
	// Verify L() returns a non-nil logger
	lg := L()
	assert.NotNil(t, lg, "default logger should be initialized")

	// Verify S() returns a non-nil sugar logger
	sugar := S()
	assert.NotNil(t, sugar, "default sugar logger should be initialized")
}

// TestSugarMethods tests that the Sugar wrapper provides access to common logging methods.
func TestSugarMethods(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	SetZap(lg)

	sugar := S()

	// Test various logging methods
	sugar.Debug("debug message")
	sugar.Info("info message")
	sugar.Warn("warn message")
	sugar.Error("error message")

	// Sync to ensure output is written
	_ = lg.Sync()

	output := buf.String()
	assert.Contains(t, output, "debug message")
	assert.Contains(t, output, "info message")
	assert.Contains(t, output, "warn message")
	assert.Contains(t, output, "error message")
}

// TestDefaultZapConfigEncoderConfig tests the encoder configuration details.
func TestDefaultZapConfigEncoderConfig(t *testing.T) {
	cfg := DefaultZapConfig()

	// Verify all encoder config fields
	enc := cfg.EncoderConfig
	assert.Equal(t, "ts", enc.TimeKey)
	assert.Equal(t, "level", enc.LevelKey)
	assert.Equal(t, "logger", enc.NameKey)
	assert.Equal(t, "caller", enc.CallerKey)
	assert.Equal(t, "msg", enc.MessageKey)
	assert.Equal(t, "stacktrace", enc.StacktraceKey)
	assert.Equal(t, zapcore.DefaultLineEnding, enc.LineEnding)
	assert.NotNil(t, enc.EncodeLevel)
	assert.NotNil(t, enc.EncodeTime)
	assert.NotNil(t, enc.EncodeDuration)
	assert.NotNil(t, enc.EncodeCaller)
}

// TestSetZapReplacement tests that SetZap properly replaces the logger.
func TestSetZapReplacement(t *testing.T) {
	// Create two different loggers
	var buf1, buf2 bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())

	core1 := zapcore.NewCore(encoder, zapcore.AddSync(&buf1), zap.DebugLevel)
	lg1 := zap.New(core1)

	core2 := zapcore.NewCore(encoder, zapcore.AddSync(&buf2), zap.DebugLevel)
	lg2 := zap.New(core2)

	// Set first logger and log
	SetZap(lg1)
	L().Info("message to first logger")
	_ = lg1.Sync()

	// Set second logger and log
	SetZap(lg2)
	L().Info("message to second logger")
	_ = lg2.Sync()

	// Verify messages went to correct buffers
	assert.Contains(t, buf1.String(), "message to first logger")
	assert.NotContains(t, buf1.String(), "message to second logger")

	assert.Contains(t, buf2.String(), "message to second logger")
	assert.NotContains(t, buf2.String(), "message to first logger")
}

// TestSugarPrintfWithVariousFormats tests Printf with different format specifiers.
func TestSugarPrintfWithVariousFormats(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	SetZap(lg)

	sugar := S()

	// Test various format specifiers
	sugar.Printf("string: %s", "hello")
	sugar.Printf("int: %d", 42)
	sugar.Printf("float: %.2f", 3.14)
	sugar.Printf("bool: %t", true)
	sugar.Printf("pointer: %p", &buf)

	_ = lg.Sync()

	output := buf.String()
	assert.Contains(t, output, "string: hello")
	assert.Contains(t, output, "int: 42")
	assert.Contains(t, output, "float: 3.14")
	assert.Contains(t, output, "bool: true")
}

// TestLoggerGlobalReplacement verifies that SetZap also replaces the zap global logger.
func TestLoggerGlobalReplacement(t *testing.T) {
	var buf bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zap.DebugLevel)
	lg := zap.New(core)

	SetZap(lg)

	// Verify zap.L() also returns our logger (due to ReplaceGlobals)
	zap.L().Info("via global logger")
	_ = lg.Sync()

	assert.Contains(t, buf.String(), "via global logger")
}

// TestParseLogLevel verifies log level parsing for all valid levels.
func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected zapcore.Level
	}{
		{"debug lowercase", "debug", zapcore.DebugLevel},
		{"debug uppercase", "DEBUG", zapcore.DebugLevel},
		{"debug with spaces", "  debug  ", zapcore.DebugLevel},
		{"info lowercase", "info", zapcore.InfoLevel},
		{"info uppercase", "INFO", zapcore.InfoLevel},
		{"info with spaces", "  info  ", zapcore.InfoLevel},
		{"warn lowercase", "warn", zapcore.WarnLevel},
		{"warn uppercase", "WARN", zapcore.WarnLevel},
		{"warn with spaces", "  warn  ", zapcore.WarnLevel},
		{"error lowercase", "error", zapcore.ErrorLevel},
		{"error uppercase", "ERROR", zapcore.ErrorLevel},
		{"error with spaces", "  error  ", zapcore.ErrorLevel},
		{"unknown defaults to info", "unknown", zapcore.InfoLevel},
		{"empty defaults to info", "", zapcore.InfoLevel},
		{"invalid defaults to info", "invalid-level", zapcore.InfoLevel},
		{"mixed case defaults to info", "Warning", zapcore.InfoLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ParseLogLevel(tt.input)
			assert.Equal(t, tt.expected, result, "ParseLogLevel(%q) failed", tt.input)
		})
	}
}

// TestParseLogLevelAllCases tests ParseLogLevel with various input formats.
func TestParseLogLevelAllCases(t *testing.T) {
	t.Parallel()

	// Test all valid levels
	assert.Equal(t, zapcore.DebugLevel, ParseLogLevel("debug"))
	assert.Equal(t, zapcore.InfoLevel, ParseLogLevel("info"))
	assert.Equal(t, zapcore.WarnLevel, ParseLogLevel("warn"))
	assert.Equal(t, zapcore.ErrorLevel, ParseLogLevel("error"))

	// Test default behavior
	assert.Equal(t, zapcore.InfoLevel, ParseLogLevel(""))
	assert.Equal(t, zapcore.InfoLevel, ParseLogLevel("   "))
	assert.Equal(t, zapcore.InfoLevel, ParseLogLevel("invalid"))
	assert.Equal(t, zapcore.InfoLevel, ParseLogLevel("trace")) // Not supported, defaults to info
}

// TestParseLogLevelWithTabsAndNewlines tests parsing with various whitespace.
func TestParseLogLevelWithTabsAndNewlines(t *testing.T) {
	t.Parallel()

	assert.Equal(t, zapcore.DebugLevel, ParseLogLevel("\t\ndebug\n\t"))
	assert.Equal(t, zapcore.WarnLevel, ParseLogLevel("\twarn\n"))
	assert.Equal(t, zapcore.ErrorLevel, ParseLogLevel("\nerror\t"))
}
