package logger

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	// Global logger instance
	Logger zerolog.Logger
)

// Initialize sets up the global logger with appropriate configuration
func Initialize(logLevel string) {
	// Set time format to be more human-readable
	zerolog.TimeFieldFormat = time.RFC3339

	// Configure output
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05",
		NoColor:    false,
	}

	// Multi-writer if you want to write to file and console
	// file, err := os.OpenFile("avm.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	// if err != nil {
	// 	log.Error().Err(err).Msg("Failed to open log file")
	// 	output = consoleWriter
	// } else {
	// 	multi := zerolog.MultiLevelWriter(consoleWriter, file)
	// 	output = multi
	// }

	// Setup logger
	Logger = zerolog.New(consoleWriter).
		With().
		Timestamp().
		Caller().
		Logger()

	// Set log level
	switch logLevel {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Replace standard log with zerolog
	log.Logger = Logger
}

// Get returns the global logger instance
func Get() *zerolog.Logger {
	return &Logger
}

// GetForComponent returns a logger with a component field for better filtering
func GetForComponent(component string) zerolog.Logger {
	return Logger.With().Str("component", component).Logger()
}

// FileWriter returns a writer to a log file for optional use alongside console logging
func FileWriter(path string) (io.Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}
	return file, nil
}
