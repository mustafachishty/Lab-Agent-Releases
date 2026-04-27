package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

const GistRawURL = "https://gist.githubusercontent.com/mustafachishty/49e77a39a54b223e06ec2fe45d038552/raw/lab_guardian_config.json"

const (
	AgentVersion = "v3.0.0"
	ServiceName  = "LabGuardianAgent"
	ServiceDesc  = "Lab Guardian System Monitoring Agent (v3.0 Native)"

	// Storage locations
	AppDataDir = `C:\ProgramData\LabGuardian\data`
)

// DefaultServerURL is the production API endpoint.
const DefaultServerURL = "https://labmonitoringservergo-1f69d6677862.herokuapp.com"

// Config holds the in-memory agent configuration.
// Hydrated from SQLite on startup.
type Config struct {
	ServerURL  string
	HardwareID string
	SystemID   string
	District   string
	Tehsil     string
	LabName    string
	AuthToken  string
	PCName     string
}

func EnsureDirectories() error {
	// Try to create the directory
	if err := os.MkdirAll(AppDataDir, 0o777); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", AppDataDir, err)
	}

	// ACL FIX: Grant Everyone Full Control if we can (using icacls)
	// This ensures Service (SYSTEM) and GUI (User) can share the same DB.
	// Only works if we are elevated, but that's why we have auto-elevation.
	cmd := exec.Command("icacls", AppDataDir, "/grant", "Everyone:(OI)(CI)F", "/T", "/Q")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set ACL on %s: %w", AppDataDir, err)
	}

	return nil
}

func Load() (*Config, error) {
	// Simple loader that returns defaults.
	// Actual auth/system values are loaded via auth.LoadFromDB(cfg) in main.go
	return &Config{
		ServerURL: DefaultServerURL,
	}, nil
}

func FetchFailsafeConfig() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(GistRawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data struct {
		ActiveServer string `json:"active_server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.ActiveServer, nil
}
