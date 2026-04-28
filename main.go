// Lab Guardian Agent — main entrypoint.
//
// Launch modes (determined by CLI flags):
//   --service  : Run directly as a Windows Service (called by SCM)
//   --install  : Install the agent as a native Windows Service (CLI)
//   --uninstall: Remove the Windows Service (CLI)
//   --debug    : Run heartbeat loop in foreground (dev mode)
//   (none)     : Default GUI mode — auto-elevates to Admin, auto-installs if needed.
//
// Build for Windows:
//
//	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o agent.exe .
package main

import (
	"fmt"
	"log"
	"os"

	"go_lms_agent/pkg/auth"
	"go_lms_agent/pkg/config"
	"go_lms_agent/pkg/gui"
	"go_lms_agent/pkg/heartbeat"
	"go_lms_agent/pkg/service"
)

func main() {
	// Ensure data directories exist on first run
	if err := config.EnsureDirectories(); err != nil {
		log.Printf("[INIT] Warning: could not create data dirs: %v", err)
	}

	flag := ""
	if len(os.Args) > 1 {
		flag = os.Args[1]
	}

	switch flag {
	case "--service":
		// Called by the Windows Service Control Manager
		if err := service.RunAsService(); err != nil {
			log.Fatalf("[SERVICE] Fatal: %v", err)
		}

	case "--install":
		if err := service.Install(); err != nil {
			fmt.Fprintf(os.Stderr, "[INSTALL] Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[INSTALL] Lab Guardian service installed successfully.")

	case "--uninstall":
		if err := service.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "[UNINSTALL] Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[UNINSTALL] Lab Guardian service removed.")

	case "--debug":
		// Direct run (debug / development mode)
		fmt.Println("[Lab Guardian Agent] Starting in direct mode...")
		service.RunDirect()

	default:
		// GUI mode — the standard user experience.
		// Step 1: Re-launch with Admin rights if not already elevated.
		if !service.IsElevated() {
			if err := service.RelaunchAsAdmin(); err != nil {
				log.Printf("[INIT] Could not elevate: %v (continuing as-is)", err)
			} else {
				os.Exit(0)
			}
		}

		cfg, _ := config.Load()
		if cfg == nil {
			cfg = &config.Config{ServerURL: config.DefaultServerURL}
		}
		if cfg.ServerURL == "" {
			cfg.ServerURL = config.DefaultServerURL
		}

		// Step 2: Ensure Hardware ID is generated BEFORE doing anything else
		if cfg.HardwareID == "" {
			hid, err := auth.GetHardwareID()
			if err == nil && hid != "" {
				cfg.HardwareID = hid
				_ = config.Save(cfg)
			}
		}

		// Step 3: PRE-AUTH LOCK SCREEN
		// This mimics exactly the Python pre-auth screen. It blocks the main management GUI from opening
		// until the Administrator verifies the HID is actually present in the Supabase devices table.
		if !gui.ShowAuthDialog(cfg) {
			log.Printf("[SECURITY] Authentication cancelled or failed. Halting.")
			_ = service.Uninstall()
			_ = config.Wipe()
			os.Exit(1)
		}

		// Step 4: Auto-install the service if it isn't already running.
		if !service.IsRunning() {
			if err := service.Install(); err != nil {
				log.Printf("[INIT] Auto-install failed: %v", err)
			}
		}

		// Step 5: Start the Supervisor with all core services
		go service.SafeRun("AppTracker", service.StartTracking)
		go service.SafeRun("Heartbeat", func() { heartbeat.StartHeartbeat(cfg) })

		// Step 6: Open the management GUI (Main Thread)
		gui.RunGUI(cfg)
	}
}

