package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/persistence"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/yusufpapurcu/wmi"
)

type Win32_ComputerSystemProduct struct {
	UUID string
}

type AuthPayload struct {
	HardwareID string                 `json:"hardware_id"`
	Specs      map[string]interface{} `json:"specs"`
}

type AuthResponse struct {
	Status   string `json:"status"`
	Token    string `json:"token"`
	SystemID string `json:"system_id"`
	District string `json:"city"`
	Tehsil   string `json:"tehsil"`
	LabName  string `json:"lab_name"`
	PCName   string `json:"pc_name"`
	Location *struct {
		District string `json:"city"`
		Tehsil   string `json:"tehsil"`
		LabName  string `json:"lab_name"`
	} `json:"location"`
}

// GetHardwareID returns the motherboard UUID via native WMI.
func GetHardwareID() (string, error) {
	var dst []Win32_ComputerSystemProduct
	q := wmi.CreateQuery(&dst, "")
	if err := wmi.Query(q, &dst); err != nil {
		return fallbackHWID()
	}

	if len(dst) == 0 || dst[0].UUID == "" || dst[0].UUID == "00000000-0000-0000-0000-000000000000" {
		return fallbackHWID()
	}

	return dst[0].UUID, nil
}

func fallbackHWID() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "UNKNOWN-HWID", nil
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagLoopback == 0 && len(i.HardwareAddr) > 0 {
			mac := strings.ReplaceAll(i.HardwareAddr.String(), ":", "")
			return "MAC-" + strings.ToUpper(mac), nil
		}
	}
	return "UNKNOWN-HWID", nil
}

func LoadFromDB(cfg *config.Config) {
	dbURL := persistence.GetConfig("server_url")
	if dbURL != "" {
		cfg.ServerURL = dbURL
	}
	cfg.AuthToken = persistence.GetConfig("auth_token")
	cfg.SystemID = persistence.GetConfig("system_id")
	cfg.District = persistence.GetConfig("city")
	cfg.Tehsil = persistence.GetConfig("tehsil")
	cfg.LabName = persistence.GetConfig("lab_name")
	cfg.PCName = persistence.GetConfig("pc_name")
}

func Authenticate(cfg *config.Config) (*AuthResponse, error) {
	specs := getNativeSpecs()
	payload := AuthPayload{
		HardwareID: cfg.HardwareID,
		Specs:      specs,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/auth", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, err
	}

	if authResp.Status == "authorized" {
		cfg.AuthToken = authResp.Token
		cfg.SystemID = authResp.SystemID
		if authResp.PCName != "" {
			cfg.PCName = authResp.PCName
			persistence.SetConfig("pc_name", authResp.PCName)
		}

		persistence.SetConfig("auth_token", authResp.Token)
		persistence.SetConfig("system_id", authResp.SystemID)

		// Support both legacy nested response and current flat response.
		district := authResp.District
		tehsil := authResp.Tehsil
		labName := authResp.LabName
		if authResp.Location != nil {
			if authResp.Location.District != "" {
				district = authResp.Location.District
			}
			if authResp.Location.Tehsil != "" {
				tehsil = authResp.Location.Tehsil
			}
			if authResp.Location.LabName != "" {
				labName = authResp.Location.LabName
			}
		}

		if district != "" {
			cfg.District = district
			persistence.SetConfig("city", district)
		}
		if tehsil != "" {
			cfg.Tehsil = tehsil
			persistence.SetConfig("tehsil", tehsil)
		}
		if labName != "" {
			cfg.LabName = labName
			persistence.SetConfig("lab_name", labName)
		}
	}

	return &authResp, nil
}

func IsTokenValid(cfg *config.Config) bool {
	return cfg.AuthToken != ""
}

func getNativeSpecs() map[string]interface{} {
	hostname, _ := os.Hostname()
	v, _ := mem.VirtualMemory()
	c, _ := cpu.Info()
	h, _ := host.Info()

	cpuName := "Unknown CPU"
	if len(c) > 0 {
		cpuName = c[0].ModelName
	}

	return map[string]interface{}{
		"os":       fmt.Sprintf("%s %s", h.OS, h.PlatformVersion),
		"hostname": hostname,
		"cpu":      cpuName,
		"ram_gb":   v.Total / (1024 * 1024 * 1024),
		"arch":     runtime.GOARCH,
	}
}
