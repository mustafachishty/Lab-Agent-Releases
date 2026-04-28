// Package setup implements the CLI interactive registration wizard for first-time setup.
// It guides the technician through selecting District > Tehsil > Lab and binding this machine
// to an existing system slot in the Supabase device registry.
package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/config"
)

// MetaResponse is the structure returned by GET /api/get-meta.
type MetaResponse struct {
	Districts []string                       `json:"cities"`
	Hierarchy map[string]map[string][]string `json:"hierarchy"`
	Version   string                         `json:"version"`
}

// AvailableSystem is a device slot from GET /api/available-systems.
type AvailableSystem struct {
	SystemID string `json:"system_id"`
	District string `json:"city"`
	Tehsil   string `json:"tehsil"`
	LabName  string `json:"lab_name"`
}

// RunWizard runs the interactive terminal-based setup wizard.
func RunWizard() error {
	fmt.Println("=========================================")
	fmt.Println("  Lab Guardian Agent — Setup Wizard")
	fmt.Println("=========================================")
	fmt.Println("")

	// Determine HWID
	hid, err := auth.GetHardwareID()
	if err != nil {
		return fmt.Errorf("cannot read Hardware ID: %w", err)
	}
	fmt.Printf("Hardware ID (HWID): %s\n", hid)
	fmt.Println("")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.HardwareID == "" {
		cfg.HardwareID = hid
	}

	// Prompt for server URL
	scanner := bufio.NewScanner(os.Stdin)
	serverURL := prompt(scanner, fmt.Sprintf("Server URL [%s]: ", config.DefaultServerURL))
	if serverURL == "" {
		serverURL = config.DefaultServerURL
	}
	cfg.ServerURL = serverURL

	client := &http.Client{Timeout: 20 * time.Second}

	// Fetch location hierarchy from server
	fmt.Println("\nFetching lab registry from server...")
	meta, err := fetchMeta(client, serverURL)
	if err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}
	fmt.Printf("Server version: %s\n\n", meta.Version)

	// District selection
	fmt.Println("Available Districts:")
	for i, d := range meta.Districts {
		fmt.Printf("  [%d] %s\n", i+1, d)
	}
	distIdx := promptInt(scanner, "Select district number: ", 1, len(meta.Districts)) - 1
	selectedDistrict := meta.Districts[distIdx]
	cfg.District = selectedDistrict

	// Tehsil selection
	tehsils := sortedKeys(meta.Hierarchy[selectedDistrict])
	fmt.Printf("\nTehsils in %s:\n", selectedDistrict)
	for i, t := range tehsils {
		fmt.Printf("  [%d] %s\n", i+1, t)
	}
	tehsilIdx := promptInt(scanner, "Select tehsil number: ", 1, len(tehsils)) - 1
	selectedTehsil := tehsils[tehsilIdx]
	cfg.Tehsil = selectedTehsil

	// Lab selection
	labs := meta.Hierarchy[selectedDistrict][selectedTehsil]
	fmt.Printf("\nLabs in %s > %s:\n", selectedDistrict, selectedTehsil)
	for i, l := range labs {
		fmt.Printf("  [%d] %s\n", i+1, l)
	}
	labIdx := promptInt(scanner, "Select lab number: ", 1, len(labs)) - 1
	selectedLab := labs[labIdx]
	cfg.LabName = selectedLab

	// Fetch available system slots for this lab
	fmt.Printf("\nFetching available PC slots for lab: %s...\n", selectedLab)
	slots, err := fetchAvailableSystems(client, serverURL)
	if err != nil {
		return fmt.Errorf("fetch available systems: %w", err)
	}

	// Filter by lab
	var matching []AvailableSystem
	for _, s := range slots {
		if strings.EqualFold(s.District, selectedDistrict) &&
			strings.EqualFold(s.LabName, selectedLab) {
			matching = append(matching, s)
		}
	}

	if len(matching) == 0 {
		return fmt.Errorf("no available PC slots found for this lab. Please add slots in the Dashboard first")
	}

	fmt.Printf("\nAvailable PC slots in %s:\n", selectedLab)
	for i, s := range matching {
		fmt.Printf("  [%d] %s\n", i+1, s.SystemID)
	}
	slotIdx := promptInt(scanner, "Select PC slot number: ", 1, len(matching)) - 1
	selectedSlot := matching[slotIdx]
	cfg.SystemID = selectedSlot.SystemID

	pcName, _ := os.Hostname()

	// Bind this hardware to the selected slot
	fmt.Printf("\nBinding HWID %s to system slot %s...\n", hid, selectedSlot.SystemID)
	if err := bindSystem(client, serverURL, hid, selectedSlot.SystemID); err != nil {
		return fmt.Errorf("bind failed: %w", err)
	}

	cfg.PCName = pcName
	cfg.HardwareID = hid

	// Authenticate and get token
	fmt.Println("\nAuthenticating...")
	resp, err := auth.Authenticate(cfg)
	if err != nil || resp.Status != "authorized" {
		fmt.Printf("[WARN] Auth returned: %v (status: %s). Token will be retried at runtime.\n", err, resp.Status)
	} else {
		fmt.Println("Authentication successful.")
	}

	// Save config
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Println("\n=========================================")
	fmt.Printf("  Setup Complete!\n")
	fmt.Printf("  System ID : %s\n", cfg.SystemID)
	fmt.Printf("  District  : %s\n", cfg.District)
	fmt.Printf("  Tehsil    : %s\n", cfg.Tehsil)
	fmt.Printf("  Lab       : %s\n", cfg.LabName)
	fmt.Printf("  PC Name   : %s\n", cfg.PCName)
	fmt.Println("=========================================")
	fmt.Println("\nRun 'agent.exe --install' to install as a Windows Service.")
	return nil
}

// ---------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------

func fetchMeta(client *http.Client, serverURL string) (*MetaResponse, error) {
	resp, err := client.Get(serverURL + "/api/get-meta")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var meta MetaResponse
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func fetchAvailableSystems(client *http.Client, serverURL string) ([]AvailableSystem, error) {
	resp, err := client.Get(serverURL + "/api/available-systems")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var slots []AvailableSystem
	if err := json.Unmarshal(raw, &slots); err != nil {
		return nil, err
	}
	return slots, nil
}

func bindSystem(client *http.Client, serverURL, hid, sysID string) error {
	payload := map[string]string{"hardware_id": hid, "system_id": sysID}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(serverURL+"/api/bind", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

// ---------------------------------------------------------------
// Input helpers
// ---------------------------------------------------------------

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

func promptInt(scanner *bufio.Scanner, label string, min, max int) int {
	for {
		input := prompt(scanner, label)
		n, err := strconv.Atoi(input)
		if err == nil && n >= min && n <= max {
			return n
		}
		fmt.Printf("Please enter a number between %d and %d.\n", min, max)
	}
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple sort
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
