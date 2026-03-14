package kea_traefik_plugin

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type logLevel int

const (
	logLevelNone logLevel = iota
	logLevelErr
	logLevelInfo
)

var logMu sync.Mutex

func logLevelName(level logLevel) string {
	switch level {
	case logLevelNone:
		return "None"
	case logLevelInfo:
		return "Info"
	default:
		return "Err"
	}
}

func infof(format string, args ...any) {
	if globalLogLevel < logLevelInfo {
		return
	}
	writeLog(os.Stdout, "INF", format, args...)
}

func errf(format string, args ...any) {
	if globalLogLevel < logLevelErr {
		return
	}
	writeLog(os.Stderr, "ERR", format, args...)
}

func writeLog(stream *os.File, level, format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()

	timestamp := time.Now().Format(time.RFC3339)
	message := fmt.Sprintf(format, args...)
	fmt.Fprintf(stream, "%s %s [kea] %s\n", timestamp, level, message)
}
