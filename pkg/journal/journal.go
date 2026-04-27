package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/persistence"
	"labguardian/agent/pkg/telemetry"
)

type Entry struct {
	Date           string                `json:"date"`
	SystemID       string                `json:"system_id"`
	HardwareID     string                `json:"hardware_id"`
	District       string                `json:"city"`
	Tehsil         string                `json:"tehsil"`
	LabName        string                `json:"lab_name"`
	CPUScore       float64               `json:"cpu_score"`
	RuntimeMinutes int                   `json:"runtime_minutes"`
	StartTime      string                `json:"start_time"`
	EndTime        string                `json:"end_time"`
	AppUsage       telemetry.AppUsageMap `json:"app_usage"`
}

func Store(cfg *config.Config, snap *telemetry.Snapshot, usage telemetry.AppUsageMap, now time.Time, runtimeMins int) {
	startTime := persistence.GetState("first_start_time")
	if startTime == "" {
		startTime = now.Format(time.RFC3339)
	}

	entry := Entry{
		Date:           now.Format("2006-01-02"),
		SystemID:       cfg.SystemID,
		HardwareID:     cfg.HardwareID,
		District:       cfg.District,
		Tehsil:         cfg.Tehsil,
		LabName:        cfg.LabName,
		CPUScore:       snap.CPUPercent,
		RuntimeMinutes: runtimeMins,
		StartTime:      startTime,
		EndTime:        now.Format(time.RFC3339),
		AppUsage:       usage,
	}

	data, _ := json.Marshal(entry)
	persistence.AddJournal(string(data))
}

func SyncPending(cfg *config.Config) {
	journals, err := persistence.GetPendingJournals()
	if err != nil || len(journals) == 0 {
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, j := range journals {
		var entry Entry
		if err := json.Unmarshal([]byte(j.Data), &entry); err != nil {
			persistence.DeleteJournal(j.ID)
			continue
		}

		if err := syncEntry(client, cfg, entry); err == nil {
			persistence.DeleteJournal(j.ID)
			log.Printf("[JOURNAL] Synced entry ID: %d", j.ID)
		} else {
			log.Printf("[JOURNAL] Sync failed for ID %d: %v", j.ID, err)
		}
	}
}

func syncEntry(client *http.Client, cfg *config.Config, e Entry) error {
	payload := map[string]interface{}{
		"system_id":       e.SystemID,
		"hardware_id":     e.HardwareID,
		"date":            e.Date,
		"city":            e.District,
		"tehsil":          e.Tehsil,
		"lab_name":        e.LabName,
		"cpu_score":       e.CPUScore,
		"runtime_minutes": e.RuntimeMinutes,
		"start_time":      e.StartTime,
		"end_time":        e.EndTime,
		"app_usage":       e.AppUsage,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/sync-offline-data", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server status: %d", resp.StatusCode)
	}
	return nil
}

func CleanOldEntries(maxDays int) {
	persistence.CleanOldJournals(maxDays)
}
