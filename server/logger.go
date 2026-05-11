package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const LogFileName = "general-mcp-server.log"

var (
	appLogger   *log.Logger
	appLogFile  *os.File
	appLogMutex sync.RWMutex
)

func InitLogger(logDir string) error {
	appLogMutex.Lock()
	defer appLogMutex.Unlock()

	if appLogFile != nil {
		appLogFile.Close()
	}

	logPath := filepath.Join(logDir, LogFileName)

	var err error
	appLogFile, err = os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		appLogger = log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	appLogger = log.New(appLogFile, "", log.LstdFlags|log.Lmicroseconds)
	return nil
}

func Logger() *log.Logger {
	appLogMutex.RLock()
	defer appLogMutex.RUnlock()

	if appLogger == nil {
		return log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	}
	return appLogger
}

func WriteDebugLog(format string, args ...interface{}) {
	appLogMutex.RLock()
	defer appLogMutex.RUnlock()

	logger := appLogger
	if logger == nil {
		log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds).Printf(format, args...)
		return
	}

	logger.Printf(format, args...)

	if appLogFile != nil {
		appLogFile.Sync()
	}
}

func CloseLogger() {
	appLogMutex.Lock()
	defer appLogMutex.Unlock()

	if appLogFile != nil {
		appLogFile.Close()
		appLogFile = nil
	}
	appLogger = nil
}
