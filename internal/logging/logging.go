// Package logging constructs procmesh loggers.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New constructs a logger that writes the requested format at the requested level.
func New(w io.Writer, format, level string) (*slog.Logger, error) {
	if w == nil {
		return nil, fmt.Errorf("log writer is nil")
	}

	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("log level %q is unsupported", level)
	}

	options := &slog.HandlerOptions{Level: slogLevel}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, options)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, options)), nil
	default:
		return nil, fmt.Errorf("log format %q is unsupported", format)
	}
}
