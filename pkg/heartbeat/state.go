package heartbeat

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/telemetry"
	"github.com/shirou/gopsutil/v3/host"
)

// State holds session data that should survive an agent update/restart.
// It survives reboots ONLY if the day hasn't changed.
type State struct {
	LastDate          string                `json:"last_date"`           // YYYY-MM-DD
	TotalDailyMinutes int                   `json:"total_daily_minutes"` // Cumulative minutes for the day
	CumulativeApps    telemetry.AppUsageMap `json:"cumulative_apps"`     // Cumulative app usage for the day
	LastBootTime      uint64                `json:"last_boot_time"`      // Tracks current boot session
	FirstStartTime    time.Time             `json:"first_start_time"`    // The very first start of the day
}


var stateFile = filepath.Join(config.AppDataDir, "state.json")

// LoadState attempts to recover session data from disk.
func LoadState() *State {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	currentBoot, _ := host.BootTime()

	defaultState := &State{
		LastDate:          today,
		TotalDailyMinutes: 0,
		CumulativeApps:    make(telemetry.AppUsageMap),
		LastBootTime:      currentBoot,
		FirstStartTime:    now,
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return defaultState
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[STATE] Corrupt state file, starting fresh: %v", err)
		return defaultState
	}

	// Check if the day has changed since the last save
	if s.LastDate != today {
		log.Printf("[STATE] New day detected (%s -> %s). Resetting daily counters.", s.LastDate, today)
		return defaultState
	}

	// If same day, we keep the counters even if the boot time changed.
	log.Printf("[STATE] Restored Same-Day Session: %d mins already tracked today. Apps: %d", s.TotalDailyMinutes, len(s.CumulativeApps))
	s.LastBootTime = currentBoot // Sync current boot
	return &s
}

// SaveState persists the current daily totals to disk.
func (r *Runner) SaveState() {
	// Our daily total is what we started with + what we earned this session
	currentSessionMins := int(time.Since(r.localStartTime).Minutes())

	s := State{
		LastDate:          time.Now().UTC().Format("2006-01-02"),
		TotalDailyMinutes: r.storedDailyMins + currentSessionMins,
		CumulativeApps:    r.cumulativeApps,
		LastBootTime:      r.bootTime,
		FirstStartTime:    r.startTime, // Use the Runner's stable start time
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}

	_ = os.MkdirAll(filepath.Dir(stateFile), 0755)
	_ = os.WriteFile(stateFile, data, 0644)
}


