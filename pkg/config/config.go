// Package config manages persistent agent configuration.
// Config is stored at C:\ProgramData\LabGuardianAgent\config.json
// The JSON structure is fully compatible with the Python agent's config format.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	AgentVersion = "5.2.2"
	ServiceName  = "LabGuardianAgent"

	ServiceDesc  = "Lab Guardian System Monitoring Agent"

	// AppDataDir is the primary storage location. Requires Admin rights on Windows.
	AppDataDir = `C:\ProgramData\LabGuardianAgent`

	// InstallDir is where the agent binary is permanently installed (like AnyDesk).
	InstallDir  = `C:\Program Files\LabGuardianAgent`
	InstalledExe = InstallDir + `\agent.exe`
)

var (
	configFile       = filepath.Join(AppDataDir, "config.json")
	LogFile          = filepath.Join(AppDataDir, "agent.log")
	JournalDir       = filepath.Join(AppDataDir, "journal")
	CacheDir         = filepath.Join(AppDataDir, "cache")
	MetricsCacheFile = filepath.Join(CacheDir, "metrics.json")
)

// Config holds the persistent agent configuration.
type Config struct {
	ServerURL   string `json:"server_url"`
	HardwareID  string `json:"hardware_id"`
	SystemID    string `json:"system_id"`
	District    string `json:"city"` // Keep 'city' tag for backward server parity
	Tehsil      string `json:"tehsil"`
	LabName     string `json:"lab_name"`
	PCName      string `json:"pc_name"`
	AuthToken   string `json:"auth_token"`
	TokenExpiry string `json:"token_expiry"`
}

var (
	mu  sync.RWMutex
	cfg *Config
)

// DefaultServerURL is the production API endpoint (Go server on Heroku).
const DefaultServerURL = "https://labmonitoringservergo-1f69d6677862.herokuapp.com"

// EnsureDirectories creates all required storage directories.
func EnsureDirectories() error {
	for _, dir := range []string{AppDataDir, JournalDir, CacheDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}

// Load reads the config from disk. Returns empty Config if file does not exist.
func Load() (*Config, error) {
	mu.RLock()
	if cfg != nil {
		defer mu.RUnlock()
		return cfg, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()

	c := &Config{ServerURL: DefaultServerURL}
	data, err := os.ReadFile(configFile)
	if os.IsNotExist(err) {
		cfg = c
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// For dev mode, if server URL is empty, use default
	if c.ServerURL == "" {
		c.ServerURL = DefaultServerURL
	}
	cfg = c
	return c, nil
}

// Save persists the configuration to disk atomically.
func Save(c *Config) error {
	mu.Lock()
	defer mu.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Write to temp file, then rename for atomicity
	tmp := configFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("PERMISSION ERROR: Cannot write to %s. Try running Agent as Administrator. Error: %w", configFile, err)
	}
	if err := os.Rename(tmp, configFile); err != nil {
		return fmt.Errorf("ACCESS DENIED: Cannot save config. Try running Agent as Administrator. Error: %w", err)
	}
	log.Printf("[CONFIG] Successfully saved to %s", configFile)
	cfg = c
	return nil
}

// Invalidate removes the in-memory config cache, forcing the next Load() to re-read disk.
func Invalidate() {
	mu.Lock()
	cfg = nil
	mu.Unlock()
}

// Wipe deletes the persistent config file from disk to clear registration state.
func Wipe() error {
	mu.Lock()
	defer mu.Unlock()
	cfg = nil
	return os.Remove(configFile)
}
