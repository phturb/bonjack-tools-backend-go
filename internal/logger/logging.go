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
