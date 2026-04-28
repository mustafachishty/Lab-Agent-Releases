package tracker

import (
	"log"
	"syscall"
	"time"
	"unsafe"

	"labguardian/agent/pkg/db"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
)

var (
	currentApp string
	startTime  time.Time
)

// GetActiveApp returns the title of the currently focused window on Windows.
func GetActiveApp() string {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "Idle"
	}

	b := make([]uint16, 200)
	_, _, _ = procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	title := syscall.UTF16ToString(b)

	if title == "" {
		return "System"
	}
	return title
}

// TrackApp monitors app changes and saves finished sessions to DB.
func TrackApp(newApp string) {
	now := time.Now()

	// Initial start
	if currentApp == "" {
		currentApp = newApp
		startTime = now
		return
	}

	// If app changed, save the previous session
	if newApp != currentApp {
		saveSession(currentApp, startTime, now)
		currentApp = newApp
		startTime = now
	}
}

func saveSession(app string, start, end time.Time) {
	duration := int(end.Sub(start).Seconds())
	if duration < 1 {
		return // Ignore micro-switches
	}

	_, err := db.DB.Exec(`
		INSERT INTO app_usage (app_name, start_time, end_time, duration, status)
		VALUES (?, ?, ?, ?, 'pending')`,
		app, start.Format(time.RFC3339), end.Format(time.RFC3339), duration,
	)

	if err != nil {
		log.Printf("[TRACKER] Failed to save session: %v", err)
	}
}
