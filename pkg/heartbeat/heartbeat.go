// Package heartbeat manages the 30-second telemetry upload cycle.
// It sends CPU, RAM, Disk, GPU, and application usage data to the server,
// handles the C2 command response, and triggers OTA updates if a newer
// version string is returned.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/commands"
	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/journal"
	"labguardian/agent/pkg/telemetry"
	"labguardian/agent/pkg/updater"
)

const heartbeatInterval = 30 * time.Second

// Payload is the JSON body sent to POST /api/heartbeat.
type Payload struct {
	HardwareID     string                  `json:"hardware_id"`
	SystemID       string                  `json:"system_id"`
	Status         string                  `json:"status"`
	Version        string                  `json:"version"`
	PendingLogs    int                     `json:"pending_logs"`
	CPUScore       float64                 `json:"cpu_score"`
	RuntimeMinutes int                     `json:"runtime_minutes"`
	LastActive     string                  `json:"last_active"`
}

func StartHeartbeat(cfg *config.Config) {
	for {
		beat(cfg)
		time.Sleep(30 * time.Second)
	}
}

func beat(cfg *config.Config) {
	snap, _ := telemetry.Collect()
	
	// Count pending records in SQLite
	var pendingCount int
	db.DB.QueryRow("SELECT COUNT(*) FROM sync_queue").Scan(&pendingCount)

	payload := Payload{
		HardwareID:  cfg.HardwareID,
		SystemID:    cfg.SystemID,
		Status:      "online",
		Version:     config.AgentVersion,
		PendingLogs: pendingCount,
		CPUScore:    snap.CPUPercent,
		LastActive:  time.Now().Format(time.RFC3339),
	}

	resp, err := sendHeartbeat(cfg, payload)
	if err != nil {
		return
	}

	// Handle Remote Commands (C2)
	if resp.Command != nil && string(resp.Command) != "null" {
		var cmd struct {
			ID      string `json:"id"`
			Command string `json:"cmd"`
		}
		if err := json.Unmarshal(resp.Command, &cmd); err == nil {
			executeCommand(cmd.Command)
		}
	}
}

func executeCommand(cmd string) {
	log.Printf("[C2] Executing remote command: %s", cmd)
	switch cmd {
	case "restart":
		exec.Command("powershell", "-Command", "Restart-Service LabGuardianAgent").Run()
	}
}

// Response is the JSON returned from POST /api/heartbeat.
type Response struct {
	Status        string          `json:"status"`
	SystemID      string          `json:"system_id"`
	LatestVersion string          `json:"latest_version"`
	LatestHash    string          `json:"latest_hash"`
	Command       json.RawMessage `json:"command"`
}

func sendHeartbeat(cfg *config.Config, payload Payload) (*Response, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/heartbeat", bytes.NewReader(body))
	if err != nil { return nil, err }

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Runner manages the heartbeat loop state.
type Runner struct {
	cfg            *config.Config
	tracker        *telemetry.ProcessTracker
	cumulativeApps telemetry.AppUsageMap
	startTime      time.Time // Stable start time for the entire day
	storedDailyMins int       // Minutes pre-loaded from state.json
	localStartTime  time.Time // When this process started
	lastBeat       time.Time
	httpClient     *http.Client
	stopChan       chan struct{}
	isUpdating     bool   // Lock to prevent overlapping updates
	bootTime       uint64 // System boot timestamp
}

// New creates a new heartbeat Runner.
func New(cfg *config.Config) *Runner {
	state := LoadState()

	return &Runner{
		cfg:             cfg,
		tracker:         telemetry.NewProcessTracker(5 * time.Second),
		cumulativeApps:  state.CumulativeApps,
		startTime:       state.FirstStartTime,
		storedDailyMins: state.TotalDailyMinutes,
		localStartTime:  time.Now().UTC(),
		bootTime:        state.LastBootTime,
		httpClient:      &http.Client{Timeout: 25 * time.Second},
		stopChan:        make(chan struct{}),
	}
}


// Run starts the heartbeat loop. This is a blocking call.
// Designed to be run as the main goroutine of the agent service.
func (r *Runner) Run() {
	// Clean up journal files older than 7 days (matches Python behaviour)
	journal.CleanOldEntries(7)

	// Ensure authenticated before starting
	r.ensureAuth()

	ticker := time.NewTicker(heartbeatInterval)
	// Track process usage every 5 seconds in the background
	procTicker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer procTicker.Stop()

	log.Printf("[HEARTBEAT] Loop started. Interval: %s", heartbeatInterval)

	// Watchdog: restart stuck heartbeat if it hasn't fired in 3x interval
	go r.watchdog()

	for {
		select {
		case <-procTicker.C:
			r.tracker.Tick()
			r.updateLocalMetricsCache()

		case <-ticker.C:
			r.lastBeat = time.Now()
			r.beat()

		case <-r.stopChan:
			log.Println("[HEARTBEAT] Stop signal received. Performing last-breath heartbeat...")
			r.statusBeat("offline")
			return
		}
	}
}

// beat performs a single heartbeat cycle.
func (r *Runner) beat() {
	// Re-authenticate if token is stale
	if !auth.IsTokenValid(r.cfg) {
		r.ensureAuth()
	}

	snap, err := telemetry.Collect()
	if err != nil {
		log.Printf("[HEARTBEAT] Telemetry error: %v", err)
	}

	now := time.Now().UTC()
	runtimeMins := r.storedDailyMins + int(now.Sub(r.localStartTime).Minutes())

	appUsage := r.tracker.Snapshot() // delta snapshot (resets counter)

	// Add delta to the daily cumulative tracker for the GUI
	for k, v := range appUsage {
		r.cumulativeApps[k] += v
	}

	// Inject real-time CPU load for dashboard live display (mirrors Python agent)
	appUsage["__current_cpu__"] = int(snap.CPUPercent)

	payload := Payload{
		HardwareID:     r.cfg.HardwareID,
		SystemID:       r.cfg.SystemID,
		District:       r.cfg.District,
		Tehsil:         r.cfg.Tehsil,
		LabName:        r.cfg.LabName,
		PCName:         r.cfg.PCName,
		Status:         "online",
		CPUScore:       snap.CPUPercent,
		RuntimeMinutes: runtimeMins,
		SessionStart:   r.startTime.Format(time.RFC3339),
		LastActive:     now.Format(time.RFC3339),
		AppUsage:       appUsage,
		IsDelta:        true,
		Specs:          snap.Specs,
	}

	resp, err := r.sendHeartbeat(payload)
	if err != nil {
		log.Printf("[HEARTBEAT] Send error (offline): %v", err)
		// Journal this heartbeat for later sync
		journal.Store(r.cfg, snap, appUsage, now, runtimeMins)
		return
	}


	// --- SELF-DESTRUCT MECHANISM ---
	// If the server rejects the system (row deleted from DB)
	if resp != nil && resp.Status == "unregistered" {
		log.Println("[HEARTBEAT] Server reported UNREGISTERED. Commencing background self-destruct.")
		// 1. Remove the background service so it never runs again
		_ = exec.Command("sc", "stop", config.ServiceName).Run()
		_ = exec.Command("sc", "delete", config.ServiceName).Run()
		// 2. Wipe the program data directory (destroys tokens, logs, config)
		_ = os.RemoveAll(config.AppDataDir)
		// 3. Forcibly close the GUI if it is open (since it's a separate process)
		_ = exec.Command("taskkill", "/F", "/IM", "agent.exe").Run()
		// 4. Terminate self
		os.Exit(0)
	}

	// Sync any queued offline data
	go journal.SyncPending(r.cfg)

	// Handle C2 command if present
	if resp.Command != nil && string(resp.Command) != "null" {
		go commands.Execute(r.cfg, resp.Command)
	}

	// OTA update check
	if resp.LatestVersion != "" && resp.LatestVersion != config.AgentVersion {
		if r.isUpdating {
			log.Printf("[UPDATER] Update to %s already in progress. Skipping trigger.", resp.LatestVersion)
		} else {
			log.Printf("[UPDATER] New version available: %s (current: %s)", resp.LatestVersion, config.AgentVersion)
			r.isUpdating = true
			go func() {
				defer func() { r.isUpdating = false }()
				updater.Update(r.cfg, resp.LatestVersion, resp.LatestHash)
			}()
		}
	}

	// Persist state locally after a successful cycle
	r.SaveState()
}

// statusBeat performs a lightweight heartbeat with a specific status override (e.g., "offline").
func (r *Runner) statusBeat(status string) {
	snap, _ := telemetry.Collect()
	now := time.Now().UTC()
	payload := Payload{
		HardwareID:     r.cfg.HardwareID,
		SystemID:       r.cfg.SystemID,
		District:       r.cfg.District,
		Tehsil:         r.cfg.Tehsil,
		LabName:        r.cfg.LabName,
		PCName:         r.cfg.PCName,
		Status:         status,
		CPUScore:       snap.CPUPercent,
		RuntimeMinutes: r.storedDailyMins + int(now.Sub(r.localStartTime).Minutes()),
		SessionStart:   r.startTime.Format(time.RFC3339),
		LastActive:     now.Format(time.RFC3339),
		AppUsage:       make(telemetry.AppUsageMap),
		IsDelta:        true,
	}
	_, _ = r.sendHeartbeat(payload)
}

// Stop signals the heartbeat loop to exit and clean up.
func (r *Runner) Stop() {
	close(r.stopChan)
}

// sendHeartbeat marshals the payload and POST's it to the server.
func (r *Runner) sendHeartbeat(payload Payload) (*Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", r.cfg.ServerURL+"/api/heartbeat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.AuthToken)

	httpResp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http execute: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("server responded with status: %d", httpResp.StatusCode)
	}

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (%s)", err, string(raw))
	}
	
	if resp.Status == "unregistered" {
		log.Printf("[HEARTBEAT] Device unregistered by server. Initiating self-destruct...")
		// Use CLI call to uninstall to avoid import cycle
		exe, _ := os.Executable()
		_ = exec.Command(exe, "--uninstall").Run()
		_ = config.Wipe()
		os.Exit(0)
	}
	return &resp, nil
}

// ensureAuth blocks until the device is successfully authenticated.
func (r *Runner) ensureAuth() {
	for {
		resp, err := auth.Authenticate(r.cfg)
		if err == nil && resp != nil && resp.Status == "authorized" {
			return
		}
		if resp != nil && resp.Status == "unregistered" {
			log.Printf("[AUTH] Device not registered. HID=%s — waiting 60s...", r.cfg.HardwareID)
		} else {
			log.Printf("[AUTH] Auth failed: %v — retrying in 30s...", err)
		}
		time.Sleep(30 * time.Second)
	}
}

// watchdog monitors the heartbeat loop and logs if it becomes unresponsive.
// Mirrors Python's _watchdog thread that revives dead monitoring threads.
func (r *Runner) watchdog() {
	watchInterval := heartbeatInterval * 3 // alert if no beat in 90s
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for range ticker.C {
		if time.Since(r.lastBeat) > watchInterval {
			log.Printf("[WATCHDOG] WARNING: No heartbeat in %v — possible stall detected", watchInterval)
		}
	}
}

// updateLocalMetricsCache dumps the live total usage to a local cache file.
// This acts as the IPC bridge so the unprivileged GUI can read the service's tracking state.
func (r *Runner) updateLocalMetricsCache() {
	current := r.tracker.PeekUsage()
	display := make(map[string]int)
	
	// Add fully completed intervals
	for k, v := range r.cumulativeApps {
		display[k] = v
	}
	// Add current un-snapshotted interval
	for k, v := range current {
		display[k] += v
	}
	
	if data, err := json.Marshal(display); err == nil {
		_ = os.WriteFile(config.MetricsCacheFile, data, 0o644)
	}
}
