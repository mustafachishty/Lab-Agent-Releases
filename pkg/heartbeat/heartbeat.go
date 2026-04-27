package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/commands"
	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/journal"
	"labguardian/agent/pkg/persistence"
	"labguardian/agent/pkg/telemetry"
	"labguardian/agent/pkg/updater"
)

const (
	heartbeatInterval = 30 * time.Second
)

type Payload struct {
	HardwareID     string                `json:"hardware_id"`
	SystemID       string                `json:"system_id"`
	District       string                `json:"city"`
	Tehsil         string                `json:"tehsil"`
	LabName        string                `json:"lab_name"`
	PCName         string                `json:"pc_name"`
	Status         string                `json:"status"`
	CPUScore       float64               `json:"cpu_score"`
	RuntimeMinutes float64               `json:"runtime_minutes"`
	SessionStart   string                `json:"session_start"`
	LastActive     string                `json:"last_active"`
	AppUsage       telemetry.AppUsageMap `json:"app_usage"`
	IsDelta        bool                  `json:"is_delta"`
}

type Response struct {
	Status        string          `json:"status"`
	SystemID      string          `json:"system_id"`
	ServerTime    string          `json:"server_time"`
	LatestVersion string          `json:"latest_version"`
	LatestHash    string          `json:"latest_hash"`
	Command       json.RawMessage `json:"command"`
}

type Runner struct {
	cfg             *config.Config
	tracker         *telemetry.ProcessTracker
	cumulativeApps  telemetry.AppUsageMap
	heartbeatDelta  telemetry.AppUsageMap
	mu              sync.Mutex
	startTime       time.Time
	localStartTime  time.Time
	storedDailySecs int
	httpClient      *http.Client
	stopChan        chan struct{}
	isUpdating      bool
	IsService       bool // Track if running as service for updates
}

func New(cfg *config.Config) *Runner {
	auth.LoadFromDB(cfg)

	lastDate := persistence.GetState("last_date")
	today := time.Now().UTC().Format("2006-01-02")

	// Only clear if we HAVE a last date and it's NOT today.
	// Prevents clearing on fresh installs or after DB wipes.
	if lastDate != "" && lastDate != today {
		log.Printf("[HEARTBEAT] New day %s detected (last was %s). Clearing state.", today, lastDate)
		persistence.ClearDailyData()
		persistence.SetState("total_daily_secs", "0")
		persistence.SetState("first_start_time", time.Now().UTC().Format(time.RFC3339))
	}
	persistence.SetState("last_date", today)

	startStr := persistence.GetState("first_start_time")
	if startStr == "" {
		startStr = time.Now().UTC().Format(time.RFC3339)
		persistence.SetState("first_start_time", startStr)
	}
	startTime, _ := time.Parse(time.RFC3339, startStr)

	storedSecs, _ := strconv.Atoi(persistence.GetState("total_daily_secs"))
	usage := persistence.GetTotalAppUsage()

	return &Runner{
		cfg:             cfg,
		tracker:         telemetry.NewProcessTracker(5 * time.Second),
		cumulativeApps:  usage,
		heartbeatDelta:  make(telemetry.AppUsageMap),
		startTime:       startTime,
		storedDailySecs: storedSecs,
		localStartTime:  time.Now().UTC(),
		httpClient:      &http.Client{Timeout: 25 * time.Second},
		stopChan:        make(chan struct{}),
	}
}

func (r *Runner) Run() {
	journal.CleanOldEntries(7)
	r.ensureAuth()

	ticker := time.NewTicker(heartbeatInterval)
	procTicker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer procTicker.Stop()

	log.Printf("[HEARTBEAT] v%s Loop started (30s heartbeat, 5s tick)", config.AgentVersion)

	for {
		select {
		case <-procTicker.C:
			r.tick()
		case <-ticker.C:
			r.beat()
		case <-r.stopChan:
			log.Println("[HEARTBEAT] Stopping runner gracefully...")
			r.statusBeat("offline")
			// One final tick to save last seconds
			r.tick()
			return
		}
	}
}

func (r *Runner) Stop() {
	close(r.stopChan)
}

func (r *Runner) tick() {
	now := time.Now().UTC()
	elapsed := int(now.Sub(r.localStartTime).Seconds())
	if elapsed <= 0 {
		return
	}

	today := now.Format("2006-01-02")

	// Midnight check
	lastDate := persistence.GetState("last_date")
	if lastDate != "" && lastDate != today {
		log.Printf("[HEARTBEAT] Midnight rollover (%s -> %s). Resetting daily metrics.", lastDate, today)
		persistence.ClearDailyData()
		r.storedDailySecs = 0
		persistence.SetState("total_daily_secs", "0")
		r.mu.Lock()
		r.cumulativeApps = make(telemetry.AppUsageMap)
		r.heartbeatDelta = make(telemetry.AppUsageMap)
		r.mu.Unlock()
		newStart := now.Format(time.RFC3339)
		r.startTime = now
		persistence.SetState("first_start_time", newStart)
		persistence.SetState("last_date", today)
	}

	// Update with REAL elapsed time
	r.storedDailySecs += elapsed
	r.localStartTime = now

	// App Tracking
	activeApp := telemetry.GetForegroundWindowName()
	if activeApp != "" {
		delta := map[string]int{activeApp: elapsed}
		persistence.UpdateAppUsage(today, delta)

		r.mu.Lock()
		r.heartbeatDelta[activeApp] += elapsed
		r.cumulativeApps[activeApp] += elapsed
		r.mu.Unlock()
	}

	persistence.SetState("total_daily_secs", strconv.Itoa(r.storedDailySecs))
	r.updateLocalMetricsCache(today)
}

func (r *Runner) updateLocalMetricsCache(today string) {
	// Push latest totals to UI cache
	r.mu.Lock()
	data, _ := json.Marshal(r.cumulativeApps)
	r.mu.Unlock()
	persistence.SetConfig("metrics_cache", string(data))
}

func (r *Runner) beat() {
	if !auth.IsTokenValid(r.cfg) {
		r.ensureAuth()
	}

	snap, _ := telemetry.Collect()
	now := time.Now().UTC()

	r.mu.Lock()
	currentHBUsage := r.heartbeatDelta
	r.heartbeatDelta = make(telemetry.AppUsageMap) // Reset for next 30s
	r.mu.Unlock()

	filteredDelta := r.filterAndCap(currentHBUsage)
	runtimeMins := float64(r.storedDailySecs) / 60.0

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
		AppUsage:       filteredDelta,
		IsDelta:        true,
	}

	resp, err := r.sendHeartbeat(payload)
	if err != nil {
		journal.Store(r.cfg, snap, filteredDelta, now, int(runtimeMins))
		return
	}

	if resp.LatestVersion != "" && resp.LatestVersion != config.AgentVersion && !r.isUpdating {
		r.isUpdating = true
		go updater.Update(r.cfg, resp.LatestVersion, resp.LatestHash, r.IsService)
	}

	if len(resp.Command) > 0 && string(resp.Command) != "null" {
		go commands.Execute(r.cfg, resp.Command)
	}

	journal.SyncPending(r.cfg)
}

func (r *Runner) statusBeat(status string) {
	payload := Payload{
		HardwareID: r.cfg.HardwareID,
		SystemID:   r.cfg.SystemID,
		Status:     status,
		LastActive: time.Now().UTC().Format(time.RFC3339),
	}
	_, _ = r.sendHeartbeat(payload)
}

func (r *Runner) sendHeartbeat(p Payload) (*Response, error) {
	body, _ := json.Marshal(p)
	req, err := http.NewRequest("POST", r.cfg.ServerURL+"/api/heartbeat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.AuthToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("heartbeat HTTP %d: %s", resp.StatusCode, string(body))
	}

	var hResp Response
	if err := json.NewDecoder(resp.Body).Decode(&hResp); err != nil {
		return nil, err
	}
	return &hResp, nil
}

func (r *Runner) ensureAuth() {
	resp, err := auth.Authenticate(r.cfg)
	if err == nil && resp.Status == "authorized" {
		r.cfg.AuthToken = resp.Token
		r.cfg.SystemID = resp.SystemID
		persistence.SetConfig("auth_token", resp.Token)
		persistence.SetConfig("system_id", resp.SystemID)
	}
}

func (r *Runner) filterAndCap(usage telemetry.AppUsageMap) telemetry.AppUsageMap {
	type kv struct {
		K string
		V int
	}
	var ss []kv
	for k, v := range usage {
		if v > 0 {
			ss = append(ss, kv{k, v})
		}
	}
	sort.Slice(ss, func(i, j int) bool {
		return ss[i].V > ss[j].V
	})

	res := make(telemetry.AppUsageMap)
	limit := 100
	if len(ss) < limit {
		limit = len(ss)
	}
	for i := 0; i < limit; i++ {
		res[ss[i].K] = ss[i].V
	}
	return res
}
