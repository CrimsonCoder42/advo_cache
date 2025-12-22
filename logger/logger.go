// Package logger provides structured logging with levels
package logger

import (
	"log"
	"os"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorBlue   = "\033[34m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
)

// Log levels
const (
	InfoLevel = iota
	WarningLevel
	ErrorLevel
)

// Logger holds loggers for each level
type Logger struct {
	Level       int
	infoLogger  *log.Logger
	warnLogger  *log.Logger
	errorLogger *log.Logger
}

var logger *Logger

func init() {
	logger = &Logger{
		Level:       InfoLevel,
		infoLogger:  log.New(os.Stdout, colorBlue+"INFO: "+colorReset, log.LstdFlags),
		warnLogger:  log.New(os.Stdout, colorYellow+"WARN: "+colorReset, log.LstdFlags),
		errorLogger: log.New(os.Stdout, colorRed+"ERROR: "+colorReset, log.LstdFlags|log.Lshortfile),
	}
}

// SetLevel sets the minimum log level
func SetLevel(level int) {
	logger.Level = level
}

// Info logs at INFO level
func Info(message string) {
	if logger.Level <= InfoLevel {
		logger.infoLogger.Println(message)
	}
}

// Warning logs at WARN level
func Warning(message string) {
	if logger.Level <= WarningLevel {
		logger.warnLogger.Println(message)
	}
}

// Error logs at ERROR level
func Error(message string) {
	if logger.Level <= ErrorLevel {
		logger.errorLogger.Println(message)
	}
}
