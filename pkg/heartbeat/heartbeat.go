// Package heartbeat manages the 30-second telemetry upload cycle.
// It sends CPU, RAM, Disk, GPU, and application usage data to the server,
// handles the C2 command response, and triggers OTA updates if a newer
// version string is returned.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"go_lms_agent/pkg/config"
	"go_lms_agent/pkg/telemetry"
	"go_lms_agent/pkg/tracker"
	"go_lms_agent/pkg/updater"
)

const heartbeatInterval = 30 * time.Second

// Payload is the JSON body sent to POST /api/heartbeat.
type Payload struct {
	HardwareID     string      `json:"hardware_id"`
	SystemID       string      `json:"system_id"`
	PCName         string      `json:"pc_name"`
	City           string      `json:"city"`
	Tehsil         string      `json:"tehsil"`
	LabName        string      `json:"lab_name"`
	Status         string      `json:"status"`
	Version        string      `json:"version"`
	PendingLogs    int         `json:"pending_logs"`
	CPUScore       float64     `json:"cpu_score"`
	RuntimeMinutes int         `json:"runtime_minutes"`
	LastActive     string      `json:"last_active"`
	AppUsage       interface{} `json:"app_usage"`
	IsDelta        bool        `json:"is_delta"`
	Specs          interface{} `json:"specs"`
}

func StartHeartbeat(cfg *config.Config) {
	for {
		beat(cfg)
		time.Sleep(30 * time.Second)
	}
}

func beat(cfg *config.Config) {
	snap, _ := telemetry.Collect()
	
	// 1. Get Deltas and reset (Server Contract Fix)
	appUsage := tracker.GetDeltas()

	payload := Payload{
		HardwareID:     cfg.HardwareID,
		SystemID:       cfg.SystemID,
		PCName:         cfg.HardwareID, // Or actual PC name if stored
		City:           cfg.District,   // Mapping District config to City payload
		Tehsil:         cfg.Tehsil,
		LabName:        cfg.LabName,
		Status:         "online",
		Version:        config.AgentVersion,
		CPUScore:       snap.CPUPercent,
		RuntimeMinutes: int(time.Since(time.Now().Add(-time.Duration(snap.Uptime)*time.Second)).Minutes()),
		LastActive:     time.Now().Format(time.RFC3339),
		AppUsage:       appUsage,
		IsDelta:        true,
	}

	resp, err := sendHeartbeat(cfg, payload)
	if err != nil {
		return
	}

	// 2. Security Self-Destruct (If server says unregistered, we wipe everything)
	if resp != nil && resp.Status == "unregistered" {
		log.Println("[HEARTBEAT] Device unregistered by server. Initiating self-destruct...")
		// Uninstall the service and wipe config
		exec.Command("sc", "stop", config.ServiceName).Run()
		exec.Command("sc", "delete", config.ServiceName).Run()
		config.Wipe()
		os.Exit(0)
	}

	// 3. Handle Remote Commands (C2)
	if resp.Command != nil && string(resp.Command) != "null" {
		var cmd struct {
			ID      string `json:"id"`
			Command string `json:"cmd"`
		}
		if err := json.Unmarshal(resp.Command, &cmd); err == nil {
			executeCommand(cmd.Command)
		}
	}

	// 4. OTA Update Check (Zero-Touch Updates)
	if resp.LatestVersion != "" && resp.LatestVersion != config.AgentVersion {
		log.Printf("[UPDATER] New version available: %s (current: %s)", resp.LatestVersion, config.AgentVersion)
		go updater.Update(cfg, resp.LatestVersion, resp.LatestHash)
	}
}

func executeCommand(cmd string) {
	log.Printf("[C2] Received remote command: %s (execution disabled by plan)", cmd)
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
