package service

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/tracker"
)

// StartTracking starts the infinite loop to poll for active apps.
// This is the core logic requested for high-frequency session tracking.
func StartTracking() {
	go func() {
		for {
			app := tracker.GetActiveApp()
			tracker.TrackApp(app)
			time.Sleep(3 * time.Second)
		}
	}()
}

// RunAsService is called by main.go when the --service flag is used.
func RunAsService() error {
	// Start the tracking loop in the background
	StartTracking()
	
	// Keep the service alive (This would ideally use windows/svc, 
	// but we'll use a simple blocking loop for now to match your plan)
	select {}
}

// Install registers the agent.exe as a Windows Service.
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	
	cmd := exec.Command("sc", "create", config.ServiceName, 
		"binPath=", fmt.Sprintf(`"%s" --service`, exe),
		"start=", "auto",
		"displayName=", config.ServiceName)
	
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sc create failed: %v (%s)", err, string(out))
	}
	
	exec.Command("sc", "description", config.ServiceName, config.ServiceDesc).Run()
	exec.Command("sc", "start", config.ServiceName).Run()
	return nil
}

// Uninstall removes the Windows Service.
func Uninstall() error {
	exec.Command("sc", "stop", config.ServiceName).Run()
	cmd := exec.Command("sc", "delete", config.ServiceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sc delete failed: %v (%s)", err, string(out))
	}
	return nil
}

// IsElevated checks if the process has Administrator rights.
func IsElevated() bool {
	f, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// RelaunchAsAdmin prompts for UAC elevation and restarts the agent.
func RelaunchAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	
	// Simple PowerShell elevation trick
	verb := "runas"
	args := fmt.Sprintf(`Start-Process "%s" -Verb %s -WorkingDirectory "%s"`, exe, verb, cwd)
	return exec.Command("powershell", "-Command", args).Run()
}

// IsRunning checks if the Windows Service is active.
func IsRunning() bool {
	out, _ := exec.Command("sc", "query", config.ServiceName).Output()
	return bytes.Contains(out, []byte("RUNNING"))
}
