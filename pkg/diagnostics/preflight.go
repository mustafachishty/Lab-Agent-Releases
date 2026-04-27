package diagnostics

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/persistence"
	"labguardian/agent/pkg/service"
)

func status(ok bool) string {
	if ok {
		return "OK"
	}
	return "FAIL"
}

// RunPreflight validates the minimum runtime requirements for production deployment.
// It prints a report and returns an error if any critical checks fail.
func RunPreflight(cfg *config.Config) error {
	if cfg == nil {
		cfg = &config.Config{ServerURL: config.DefaultServerURL}
	}

	fmt.Println("=========================================")
	fmt.Println("  Lab Guardian Agent — Preflight Check")
	fmt.Println("=========================================")

	failed := 0

	// 1) Identity checks
	hasHWID := cfg.HardwareID != ""
	hasPCName := cfg.PCName != ""
	hasSystemID := cfg.SystemID != ""
	fmt.Printf("[IDENTITY] Hardware ID present: %s\n", status(hasHWID))
	fmt.Printf("[IDENTITY] PC Name present    : %s\n", status(hasPCName))
	fmt.Printf("[IDENTITY] System ID present  : %s\n", status(hasSystemID))
	if !hasHWID || !hasPCName {
		failed++
	}

	// 2) Persistence write check
	probe := time.Now().UTC().Format(time.RFC3339Nano)
	persistence.SetState("__preflight_probe__", probe)
	persistenceOk := persistence.GetState("__preflight_probe__") == probe
	fmt.Printf("[PERSISTENCE] SQLite read/write: %s\n", status(persistenceOk))
	if !persistenceOk {
		failed++
	}

	// 3) Service capability checks
	elevated := service.IsElevated()
	serviceRunning := service.IsRunning()
	fmt.Printf("[SERVICE] Elevated context    : %s\n", status(elevated))
	fmt.Printf("[SERVICE] Service running     : %s\n", status(serviceRunning))
	if !elevated {
		fmt.Println("[SERVICE] Note: install/remove service requires admin elevation.")
	}

	// 4) Server connectivity checks
	healthOK := false
	if cfg.ServerURL != "" {
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Get(cfg.ServerURL + "/api/health")
		if err == nil {
			healthOK = resp.StatusCode >= 200 && resp.StatusCode < 300
			_ = resp.Body.Close()
		}
	}
	fmt.Printf("[NETWORK] /api/health reachable: %s\n", status(healthOK))
	if !healthOK {
		failed++
	}

	// 5) Auth state checks
	hasToken := persistence.GetConfig("auth_token") != ""
	fmt.Printf("[AUTH] Cached token present   : %s\n", status(hasToken))
	if !hasToken && hasSystemID {
		fmt.Println("[AUTH] Note: token may be refreshed automatically on first heartbeat.")
	}

	// 6) Paths summary
	localDB := os.Getenv("LOCALAPPDATA") + "\\LabGuardianAgent\\agent.db"
	fmt.Printf("[PATH] ProgramData dir        : %s\n", config.AppDataDir)
	fmt.Printf("[PATH] LocalAppData DB        : %s\n", localDB)

	fmt.Println("-----------------------------------------")
	if failed == 0 {
		fmt.Println("Preflight Result: PASS")
		return nil
	}
	fmt.Printf("Preflight Result: FAIL (%d critical checks)\n", failed)
	return fmt.Errorf("preflight failed with %d critical checks", failed)
}
