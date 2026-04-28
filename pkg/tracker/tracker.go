package tracker

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
)

var (
	mu     sync.Mutex
	deltas = make(map[string]int)
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

// TrackDelta increments the counter for the active app.
// Called every 3 seconds by the supervisor.
func TrackDelta() {
	app := GetActiveApp()
	mu.Lock()
	defer mu.Unlock()
	deltas[app] += 3
}

// GetDeltas returns the accumulated usage and clears the map.
func GetDeltas() map[string]int {
	mu.Lock()
	defer mu.Unlock()
	
	current := deltas
	deltas = make(map[string]int)
	return current
}
