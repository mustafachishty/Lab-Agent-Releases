// Package journal provides offline resilience for the Lab Guardian agent.
// When the network is unavailable, telemetry snapshots are persisted as JSON
// files in the journal directory. On reconnection, all pending entries are
// synced to the server using the /api/sync-offline-data endpoint.
package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/telemetry"
)

// Entry is the structure persisted to disk during offline periods.
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

// Store persists a telemetry snapshot to disk when the server is unreachable.
func Store(cfg *config.Config, snap *telemetry.Snapshot, usage telemetry.AppUsageMap, now time.Time, runtimeMins int) {
	entry := Entry{
		Date:           now.Format("2006-01-02"),
		SystemID:       cfg.SystemID,
		HardwareID:     cfg.HardwareID,
		District:       cfg.District,
		Tehsil:         cfg.Tehsil,
		LabName:        cfg.LabName,
		CPUScore:       snap.CPUPercent,
		RuntimeMinutes: runtimeMins,
		StartTime:      now.Format(time.RFC3339),
		EndTime:        now.Format(time.RFC3339),
		AppUsage:       usage,
	}


	if err := os.MkdirAll(config.JournalDir, 0o755); err != nil {
		log.Printf("[JOURNAL] Cannot create dir: %v", err)
		return
	}

	filename := fmt.Sprintf("journal_%s_%d.json", entry.Date, now.UnixNano())
	path := filepath.Join(config.JournalDir, filename)

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		log.Printf("[JOURNAL] Marshal error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[JOURNAL] Write error: %v", err)
	}
}

// SyncPending reads all journal entries and sends them to the server.
// Successfully synced entries are deleted from disk.
func SyncPending(cfg *config.Config) {
	files, err := filepath.Glob(filepath.Join(config.JournalDir, "journal_*.json"))
	if err != nil || len(files) == 0 {
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			log.Printf("[JOURNAL] Corrupt entry %s: %v", path, err)
			os.Remove(path) // Remove corrupt files
			continue
		}

		if err := syncEntry(client, cfg, entry); err != nil {
			log.Printf("[JOURNAL] Sync failed for %s: %v (will retry)", filepath.Base(path), err)
			continue
		}

		os.Remove(path) // Successfully synced — delete
		log.Printf("[JOURNAL] Synced and removed: %s", filepath.Base(path))
	}
}

// syncEntry sends a single journal entry to the server.
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

	httpResp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode >= 400 {
		raw, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("server error %d: %s", httpResp.StatusCode, string(raw))
	}

	_, err = io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read sync response error: %w", err)
	}

	return nil
}

// CleanOldEntries removes journal files older than maxDays to keep disk clean.
// Called on agent startup — mirrors Python's LocalDataStore.cleanup_old_journals().
func CleanOldEntries(maxDays int) {
	files, err := filepath.Glob(filepath.Join(config.JournalDir, "journal_*.json"))
	if err != nil || len(files) == 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -maxDays)
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(path)
			log.Printf("[JOURNAL] Cleaned old entry: %s", filepath.Base(path))
		}
	}
}
