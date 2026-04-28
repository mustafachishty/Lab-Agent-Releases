// Package auth handles hardware identity generation and JWT authentication.
// Hardware ID is extracted via WMI from the motherboard UUID — the exact same
// approach as the Python agent, ensuring backwards compatibility with existing
// registered devices in the Supabase database.
//
// Build constraint: Windows only (uses WMI and registry APIs).
//go:build windows

package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"go_lms_agent/pkg/config"
)

// ---------------------------------------------------------------
// Hardware ID
// ---------------------------------------------------------------

// GetHardwareID returns the motherboard UUID via WMI, matching the Python agent's method.
// We use a PowerShell subprocess to query WMI, which works on all Windows versions
// without requiring CGo or external libraries.
func GetHardwareID() (string, error) {
	// Query the same WMI class the Python agent uses: Win32_ComputerSystemProduct.UUID
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-WMIObject -Class Win32_ComputerSystemProduct).UUID")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wmi query failed: %w", err)
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" || uid == "00000000-0000-0000-0000-000000000000" {
		// Fallback: use hostname + MAC address as HWID
		return fallbackHWID()
	}
	return uid, nil
}

// fallbackHWID constructs a machine-unique ID when WMI UUID is unavailable.
func fallbackHWID() (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-WMIObject -Class Win32_NetworkAdapterConfiguration | Where-Object { $_.MACAddress } | Select-Object -First 1).MACAddress")
	out, err := cmd.Output()
	if err != nil {
		return "UNKNOWN-HWID", nil
	}
	mac := strings.ReplaceAll(strings.TrimSpace(string(out)), ":", "")
	return "MAC-" + mac, nil
}

// ---------------------------------------------------------------
// JWT Authentication
// ---------------------------------------------------------------

// AuthRequest is the payload sent to POST /api/auth.
type AuthRequest struct {
	HardwareID string `json:"hardware_id"`
	District   string `json:"city,omitempty"`
	Tehsil     string `json:"tehsil,omitempty"`
	LabName    string `json:"lab_name,omitempty"`
	PCName     string `json:"pc_name,omitempty"`
}

// AuthResponse is the server's response from POST /api/auth.
type AuthResponse struct {
	Status     string `json:"status"`
	Token      string `json:"token"`
	SystemID   string `json:"system_id"`
	District   string `json:"city"`
	Tehsil     string `json:"tehsil"`
	LabName    string `json:"lab_name"`
	PCName     string `json:"pc_name"`
	Message    string `json:"message"`
	HardwareID string `json:"hardware_id"`
}

// Authenticate calls POST /api/auth with the device's hardware ID.
// On success it stores the JWT token in config.json.
// Returns the full AuthResponse for the caller to inspect status.
func Authenticate(cfg *config.Config) (*AuthResponse, error) {
	payload := AuthRequest{
		HardwareID: cfg.HardwareID,
		District:   cfg.District,
		Tehsil:     cfg.Tehsil,
		LabName:    cfg.LabName,
		PCName:     cfg.PCName,
	}

	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/auth", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server responded with status: %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read auth body: %w", err)
	}

	var authResp AuthResponse
	if err := json.Unmarshal(raw, &authResp); err != nil {
		return nil, fmt.Errorf("parse auth response: %w (%s)", err, string(raw))
	}

	if authResp.Status == "authorized" && authResp.Token != "" {
		cfg.AuthToken = authResp.Token
		cfg.SystemID = authResp.SystemID
		cfg.TokenExpiry = time.Now().Add(23 * time.Hour).Format(time.RFC3339)
		if err := config.Save(cfg); err != nil {
			log.Printf("[AUTH] Warning: could not save token: %v", err)
		}
		log.Printf("[AUTH] Authorized. System ID: %s", cfg.SystemID)
	}

	return &authResp, nil
}

// IsTokenValid checks whether the stored JWT token is still within its validity window.
func IsTokenValid(cfg *config.Config) bool {
	if cfg.AuthToken == "" || cfg.TokenExpiry == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339, cfg.TokenExpiry)
	if err != nil {
		return false
	}
	return time.Now().Before(expiry)
}
