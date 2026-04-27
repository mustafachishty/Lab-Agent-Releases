package main

import (
	"fmt"
	"log"
	"os"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/diagnostics"
	"labguardian/agent/pkg/gui"
	"labguardian/agent/pkg/heartbeat"
	"labguardian/agent/pkg/persistence"
	"labguardian/agent/pkg/service"
	"labguardian/agent/pkg/setup"
	"os/signal"
	"syscall"
)

func hydrateIdentity(cfg *config.Config) {
	if cfg == nil {
		return
	}

	if cfg.HardwareID == "" {
		if hid, err := auth.GetHardwareID(); err == nil {
			cfg.HardwareID = hid
		}
	}

	if cfg.PCName == "" {
		if name, err := os.Hostname(); err == nil {
			cfg.PCName = name
			persistence.SetConfig("pc_name", name)
		}
	}
}

func main() {
	elevatedFlag := false
	for _, arg := range os.Args {
		if arg == "--elevated" {
			elevatedFlag = true
			break
		}
	}

	if !service.IsElevated() && !elevatedFlag {
		log.Println("[MAIN] Not elevated. Attempting one-time relaunch as Admin...")
		if err := service.RelaunchAsAdmin(); err == nil {
			os.Exit(0) // Successful relaunch call, this process can exit
		}
		log.Printf("[MAIN] Relaunch request failed. Proceeding in limited mode.")
	}

	// 1. Ensure directories exist with correct ACLs (icacls)
	config.EnsureDirectories()

	// 2. Initialize ACID Persistence
	if err := persistence.Init(); err != nil {
		gui.ShowFatalError("DB Initialization Failed", err)
		log.Fatalf("[FATAL] DB Init failed: %v", err)
	}

	flag := ""
	if len(os.Args) > 1 {
		flag = os.Args[1]
	}

	switch flag {
	case "--preflight":
		cfg, _ := config.Load()
		auth.LoadFromDB(cfg)
		hydrateIdentity(cfg)
		if err := diagnostics.RunPreflight(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "[PREFLIGHT] %v\n", err)
			os.Exit(1)
		}

	case "--setup":
		if err := setup.RunWizard(); err != nil {
			fmt.Fprintf(os.Stderr, "[SETUP] Error: %v\n", err)
			os.Exit(1)
		}

	case "--service":
		cfg, _ := config.Load()
		auth.LoadFromDB(cfg)
		hydrateIdentity(cfg)

		// Dynamic Sync
		gistURL, _ := config.FetchFailsafeConfig()
		if gistURL != "" && gistURL != cfg.ServerURL {
			cfg.ServerURL = gistURL
			persistence.SetConfig("server_url", gistURL)
		}

		runner := heartbeat.New(cfg)
		runner.IsService = true
		service.Run(runner.Run, runner.Stop)

	case "--install":
		if err := service.InstallService(); err != nil {
			fmt.Fprintf(os.Stderr, "[INSTALL] Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[INSTALL] Lab Guardian Agent service installed.")

	case "--uninstall":
		if err := service.RemoveService(); err != nil {
			fmt.Fprintf(os.Stderr, "[UNINSTALL] Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[UNINSTALL] Lab Guardian Agent service removed.")

	case "--debug":
		cfg, _ := config.Load()
		auth.LoadFromDB(cfg)
		hydrateIdentity(cfg)

		runner := heartbeat.New(cfg)
		runner.Run()

	default:
		// GUI mode
		cfg, _ := config.Load()
		if cfg == nil {
			cfg = &config.Config{ServerURL: config.DefaultServerURL}
		}

		auth.LoadFromDB(cfg)
		hydrateIdentity(cfg)

		log.Println("[MAIN] Fetching failsafe config...")
		gistURL, err := config.FetchFailsafeConfig()
		if err == nil && gistURL != "" && gistURL != cfg.ServerURL {
			log.Printf("[MAIN] Updating ServerURL to: %s", gistURL)
			cfg.ServerURL = gistURL
			persistence.SetConfig("server_url", gistURL)
		}

		if cfg.SystemID != "" {
			log.Printf("[MAIN] Starting Auth Dialog for SystemID: %s", cfg.SystemID)
			if !gui.ShowAuthDialog(cfg) {
				log.Println("[MAIN] Auth Dialog failed or cancelled.")
				os.Exit(1)
			}
			log.Println("[MAIN] Auth Dialog successful.")
		}

		log.Println("[MAIN] Starting Heartbeat Runner...")
		runner := heartbeat.New(cfg)
		runner.IsService = false
		go runner.Run()

		log.Println("[MAIN] Setting up Signal Handling...")
		go func() {
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
			<-sigChan
			log.Println("[MAIN] Termination signal received.")
			runner.Stop()
			os.Exit(0)
		}()

		log.Println("[MAIN] Entering RunGUI...")
		gui.RunGUI(cfg)
		log.Println("[MAIN] GUI exited. Stopping runner.")
		runner.Stop()
	}
}
