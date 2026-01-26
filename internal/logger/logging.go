// Package logger provides structured logging utilities for the bonjack-tools-backend-go service.
// It initializes and manages application-wide logging with context-aware structured output.
//
// Key features:
//   - Structured logging with slog from Go standard library
//   - Component-based logger creation for different subsystems
//   - JSON/text format switching based on environment
//   - Context enrichment for operation tracking
//   - Log level configuration and filtering
//
// Logging configuration:
//   - Environment variables: LOG_LEVEL, LOG_FORMAT, GO_ENV
//   - Auto-detection: JSON in production, text in development
//   - Component-based context with structured fields
//   - Test optimization: Text format with debug level for readability
//
// Logger creation:
//   - ComponentLogger(): Creates loggers with component context
//   - WithContext(): Enriches loggers with request/operation context
//   - Global logger initialization for application startup
//
// Structured logging best practices:
//   - Component field for subsystem identification
//   - Error field for structured error reporting
//   - Context fields: user_id, guild_id, channel_id, operation
//   - Consistent field naming across all components
//
// The package ensures consistent, searchable, and analyzable logging across
// the entire application with proper context for debugging and monitoring.
package logger

import (
	"log/slog"
	"os"

	"github.com/phturb/bonjack-tools-backend-go/internal/config"
)

const (
	componentKey = "component"
	errorKey     = "error"
)

// InitLogger initializes the global slog logger with appropriate configuration
func InitLogger() {
	// Determine handler type based on environment
	c := config.Config()

	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: c.Logger.Level,
	}

	switch c.Logger.Format {
	case config.LogFormatJSON:
		// JSON format for production
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case config.LogFormatText:
		// Text format for development
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		// Auto-detect: JSON in production, text in development
		if c.Environment == config.ProductionEnv {
			handler = slog.NewJSONHandler(os.Stdout, opts)
		} else {
			handler = slog.NewTextHandler(os.Stdout, opts)
		}
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

type Logger struct {
	*slog.Logger
}

func DefaultLogger() *Logger {
	return &Logger{
		Logger: slog.Default(),
	}
}

func ComponentLogger(component string) *Logger {
	return DefaultLogger().WithComponent(component)
}

func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger: l.Logger.With(args...),
	}
}

// WithComponent adds component context to a logger
func (l *Logger) WithComponent(component string) *Logger {
	return l.With(componentKey, component)
}

// WithError adds error context to a logger
func (l *Logger) WithError(err error) *Logger {
	return l.With(errorKey, err)
}
