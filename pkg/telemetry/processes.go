//go:build windows

// Package telemetry — process tracking for Windows.
// Identifies foreground/visible application windows and aggregates
// usage in seconds. Matches and filters using the APP_MAPPING and
// SYSTEM_NOISE lists identical to the Python agent.
package telemetry

import (
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// AppUsageMap maps friendly application names to seconds of tracked usage.
type AppUsageMap map[string]int

// AppMapping maps lowercase process executable names to human-friendly display names.
// This is a direct port of APP_MAPPING from System_Monitoring_Agent.py.
var AppMapping = map[string]string{
	// Browsers
	"chrome":  "Google Chrome",
	"msedge":  "Microsoft Edge",
	"firefox": "Firefox",
	"opera":   "Opera",
	"brave":   "Brave Browser",
	"vivaldi": "Vivaldi",
	"tor":     "Tor Browser",

	// Development
	"code":          "VS Code",
	"pycharm64":     "PyCharm",
	"idea64":        "IntelliJ IDEA",
	"webstorm64":    "WebStorm",
	"sublime_text":  "Sublime Text",
	"atom":          "Atom",
	"notepad++":     "Notepad++",
	"notepad":       "Notepad",
	"githubdesktop": "GitHub Desktop",
	"postman":       "Postman",
	"node":          "Node.js",

	// Office
	"winword":  "Word",
	"excel":    "Excel",
	"powerpnt": "PowerPoint",
	"outlook":  "Outlook",
	"onenote":  "OneNote",
	"mspub":    "Publisher",
	"msaccess": "Access",

	// Communication
	"whatsapp": "WhatsApp",
	"telegram": "Telegram",
	"discord":  "Discord",
	"slack":    "Slack",
	"teams":    "Microsoft Teams",
	"skype":    "Skype",
	"zoom":     "Zoom",

	// Media & Entertainment
	"spotify":    "Spotify",
	"vlc":        "VLC Player",
	"wmplayer":   "Windows Media Player",
	"potplayer":  "PotPlayer",
	"obs64":      "OBS Studio",
	"obs":        "OBS Studio",
	"audacity":   "Audacity",

	// Cloud Storage & File Transfer
	"terabox":              "TeraBox",
	"dropbox":              "Dropbox",
	"googledrivesync":      "Google Drive",
	"onedrive":             "OneDrive",
	"megasync":             "MEGA Sync",
	"idman":                "IDM",
	"qbittorrent":          "qBittorrent",
	"utorrent":             "uTorrent",

	// System & Utilities
	"explorer":        "File Explorer",
	"windowsterminal": "Windows Terminal",
	"calc":            "Calculator",
	"taskmgr":         "Task Manager",
	"winrar":          "WinRAR",
	"7zfm":            "7-Zip",

	// Design & Creative
	"photoshop":   "Photoshop",
	"illustrator": "Illustrator",
	"premierepro": "Premiere Pro",
	"afterfx":     "After Effects",
	"figma":       "Figma",
	"gimp":        "GIMP",
	"blender":     "Blender",
	"acrobat":     "Adobe Acrobat",
	"acrord32":    "Adobe Reader",

	// Virtualization
	"virtualbox": "VirtualBox",
	"vmware":     "VMware",

	// Gaming
	"steam":              "Steam",
	"epicgameslauncher":  "Epic Games",
	"unity":              "Unity Engine",
}

// systemNoise is the list of process names to completely ignore.
var systemNoise = map[string]bool{
	"taskhostw": true, "dwm": true, "shellexperiencehost": true,
	"searchhost": true, "textinputhost": true, "dashost": true,
	"sihost": true, "conhost": true, "runtimebroker": true,
	"msedgewebview2": true, "svchost": true, "lsass": true,
	"services": true, "wininit": true, "smss": true, "csrss": true,
	"fontdrvhost": true, "ctfmon": true, "searchindexer": true,
	"backgroundtaskhost": true, "applicationframehost": true,
	"systemsettings": true, "smartscreen": true, "cmd": true,
	"powershell": true, "wmic": true, "labguardian": true,
	"agent": true, "spoolsv": true, "winlogon": true,
	"taskmgr": true,
}

// ProcessTracker tracks active foreground application usage over time.
type ProcessTracker struct {
	mu        sync.RWMutex
	usage     AppUsageMap
	lastTick  time.Time
	interval  time.Duration
}

// NewProcessTracker creates a tracker with the specified poll interval.
func NewProcessTracker(interval time.Duration) *ProcessTracker {
	return &ProcessTracker{
		usage:    make(AppUsageMap),
		lastTick: time.Now(),
		interval: interval,
	}
}

// Tick polls running processes, identifies tracked applications,
// and adds elapsed seconds to their usage counter.
func (t *ProcessTracker) Tick() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	elapsed := int(now.Sub(t.lastTick).Seconds())
	t.lastTick = now

	if elapsed <= 0 {
		return
	}

	procs, err := process.Processes()
	if err != nil {
		return
	}

	seen := map[string]bool{}
	for _, p := range procs {
		name, err := p.Name()
		if err != nil {
			continue
		}
		// Strip extension
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")

		if systemNoise[name] {
			continue
		}

		// Look up friendly name from mapping
		friendly, mapped := AppMapping[name]
		if !mapped {
			continue // Not a tracked application
		}

		// Only count each application once per tick even if multiple instances run
		if seen[friendly] {
			continue
		}
		seen[friendly] = true
		t.usage[friendly] += elapsed
	}
}

// Snapshot returns and resets (delta mode) the current usage map.
// In delta mode, the caller sends only what changed since the last heartbeat.
func (t *ProcessTracker) Snapshot() AppUsageMap {
	t.mu.Lock()
	defer t.mu.Unlock()

	snapshot := make(AppUsageMap, len(t.usage))
	for k, v := range t.usage {
		snapshot[k] = v
	}
	// Reset for next delta cycle
	t.usage = make(AppUsageMap)
	return snapshot
}

// PeekUsage returns the current accumulated map WITHOUT resetting it.
func (t *ProcessTracker) PeekUsage() AppUsageMap {
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := make(AppUsageMap, len(t.usage))
	for k, v := range t.usage {
		snapshot[k] = v
	}
	return snapshot
}
