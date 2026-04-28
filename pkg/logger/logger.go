package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const logPath = `C:\ProgramData\LabGuardianAgent\logs.txt`

// Log writes a message with a timestamp to the persistent log file.
func Log(msg string) {
	// Ensure directory exists
	dir := filepath.Dir(logPath)
	_ = os.MkdirAll(dir, 0755)

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer file.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("[%s] %s\n", timestamp, msg)
	
	_, _ = file.WriteString(entry)
}

// Error logs a message prefixed with ERROR for easier searching.
func Error(msg string, err error) {
	Log(fmt.Sprintf("ERROR: %s | Detail: %v", msg, err))
}

// Info logs a message prefixed with INFO.
func Info(msg string) {
	Log(fmt.Sprintf("INFO: %s", msg))
}
