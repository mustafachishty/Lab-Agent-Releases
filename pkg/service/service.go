//go:build windows

// Package service implements native Windows Service management for the Lab Guardian agent.
// Uses golang.org/x/sys/windows/svc to register and run the agent as a proper SCM service,
// eliminating the need for NSSM or any third-party service wrapper.
package service

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/heartbeat"
)

// ---------------------------------------------------------------
// Windows Service Control Manager interaction
// ---------------------------------------------------------------

// Install copies this binary to Program Files\LabGuardianAgent\ and registers
// it as a Windows Service — identical install pattern to AnyDesk/TeamViewer.
func Install() error {
	srcExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// ── 1. Create install directory ──────────────────────────────────────────
	if err := os.MkdirAll(config.InstallDir, 0o755); err != nil {
		return fmt.Errorf("create install dir (run as Admin): %w", err)
	}

	// ── 2. Copy binary to install directory ──────────────────────────────────
	destExe := config.InstalledExe
	if strings.EqualFold(srcExe, destExe) {
		log.Printf("[INSTALL] Already running from install path: %s", destExe)
	} else {
		if err := copyFile(srcExe, destExe); err != nil {
			return fmt.Errorf("copy to install dir: %w", err)
		}
		log.Printf("[INSTALL] Binary copied: %s → %s", srcExe, destExe)
	}

	// ── 3. Register Windows Service pointing to installed copy ───────────────
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w (run as Administrator)", err)
	}
	defer m.Disconnect()

	// Remove old service if it exists (to update binary path)
	if s, err := m.OpenService(config.ServiceName); err == nil {
		s.Control(svc.Stop)
		time.Sleep(1 * time.Second)
		_ = s.Delete()
		s.Close()
		time.Sleep(1 * time.Second)
	}

	s, err := m.CreateService(config.ServiceName, destExe,
		mgr.Config{
			DisplayName: config.ServiceDesc,
			Description: "Lab Guardian: Monitors workstation metrics and reports to the central dashboard. Starts automatically with Windows.",
			StartType:   mgr.StartAutomatic,
		},
		"--service",
	)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	s.Close()

	// ── 4. Set failure/restart policy (guarantee 24/7 auto-restart) ──────────
	exec.Command("sc", "failure", config.ServiceName, "reset=", "86400",
		"actions=", "restart/5000/restart/10000/restart/30000").Run()

	// ── 5. Create Start Menu shortcut via PowerShell ──────────────────────────
	createStartMenuShortcut(destExe)

	// ── 6. Start the service immediately ─────────────────────────────────────
	if err := startService(m); err != nil {
		log.Printf("[INSTALL] Warning: could not start service immediately: %v", err)
	}

	fmt.Printf("[INSTALL] Service '%s' installed to '%s' and started.\n", config.ServiceName, destExe)
	return nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 1024*1024) // 1 MB chunks
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// createStartMenuShortcut creates a Start Menu shortcut using PowerShell.
// This makes the agent searchable and accessible to administrators.
func createStartMenuShortcut(targetExe string) {
	shortcutDir := `C:\ProgramData\Microsoft\Windows\Start Menu\Programs`
	shortcutPath := shortcutDir + `\Lab Guardian Agent.lnk`

	psScript := fmt.Sprintf(`$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.Description = 'Lab Guardian Monitoring Agent'; $s.Save()`,
		shortcutPath, targetExe, filepath.Dir(targetExe))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[INSTALL] Warning: Start Menu shortcut creation failed: %v | %s", err, string(out))
	} else {
		log.Printf("[INSTALL] Start Menu shortcut created: %s", shortcutPath)
	}
}


// Uninstall stops and removes the Windows Service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(config.ServiceName)
	if err != nil {
		return fmt.Errorf("service not found: %w", err)
	}
	defer s.Close()

	// Stop first
	s.Control(svc.Stop)
	time.Sleep(2 * time.Second)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

func startService(m *mgr.Mgr) error {
	s, err := m.OpenService(config.ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Start()
}

// ---------------------------------------------------------------
// Windows Service execution handlers
// ---------------------------------------------------------------

// labGuardianService implements the svc.Handler interface required by the SCM.
type labGuardianService struct{}

func (s *labGuardianService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Initialize the agent
	cfg, runner, err := initAgent()
	if err != nil {
		log.Printf("[SERVICE] Init error: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}
	_ = cfg

	// Signal running
	changes <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	// Start heartbeat in background
	go runner.Run()

	// Listen for stop/shutdown signals from SCM
	for c := range r {
		switch c.Cmd {
		case svc.Stop, svc.Shutdown:
			log.Println("[SERVICE] Received stop signal. Shutting down runner...")
			changes <- svc.Status{State: svc.StopPending}
			runner.Stop()
			time.Sleep(2 * time.Second) // Give time for last-breath heartbeat
			return false, 0
		}
	}
	return false, 0
}

// RunAsService blocks, handing control to the Windows Service Control Manager.
// Called when the agent is launched with --service flag by the SCM.
func RunAsService() error {
	return svc.Run(config.ServiceName, &labGuardianService{})
}

// RunDirect runs the agent in a normal foreground process (useful for debugging).
func RunDirect() error {
	cfg, runner, err := initAgent()
	if err != nil {
		return err
	}
	log.Printf("[DIRECT] Running as: %s | HID: %s", cfg.PCName, cfg.HardwareID)
	runner.Run() // Blocks until process is killed
	return nil
}

// ---------------------------------------------------------------
// Shared initialization
// ---------------------------------------------------------------

// rotateLogFile checks the log file size and rotates it if it exceeds 10MB.
func rotateLogFile() {
	info, err := os.Stat(config.LogFile)
	if err != nil {
		return
	}
	// 10MB limit
	if info.Size() > 10*1024*1024 {
		oldLog := config.LogFile + ".old"
		_ = os.Remove(oldLog)
		_ = os.Rename(config.LogFile, oldLog)
	}
}

func initAgent() (*config.Config, *heartbeat.Runner, error) {
	// Rotate logs before opening
	rotateLogFile()

	// Initialize log file
	logF, err := os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(logF)
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.SetPrefix("[LG] ")

	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	// Standardize HWID detection using the auth package
	if cfg.HardwareID == "" {
		hid, err := auth.GetHardwareID()
		if err != nil {
			log.Printf("[INIT] Warning: Cannot get HWID: %v", err)
		} else {
			cfg.HardwareID = hid
		}
	}

	// Set hostname as PC name if not configured
	if cfg.PCName == "" {
		name, _ := os.Hostname()
		cfg.PCName = name
	}

	// Authenticate (will retry on failure)
	if !auth.IsTokenValid(cfg) {
		resp, err := auth.Authenticate(cfg)
		if err != nil {
			log.Printf("[INIT] Auth failed (will retry in loop): %v", err)
		} else if resp != nil && resp.Status == "unregistered" {
			log.Printf("[INIT] Device not registered. HID=%s", cfg.HardwareID)
		}
	}

	runner := heartbeat.New(cfg)
	log.Printf("[INIT] Lab Guardian v%s initialized for PC: %s (System ID: %s)", config.AgentVersion, cfg.PCName, cfg.SystemID)
	return cfg, runner, nil
}

// IsRunning Checks if the Windows service is currently in the 'Running' state.
func IsRunning() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(config.ServiceName)
	if err != nil {
		return false
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return false
	}

	return status.State == svc.Running
}

// IsElevated returns true if the current process has Administrator privileges.
// It does this by trying to open a handle to a privileged system resource.
func IsElevated() bool {
	_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	return err == nil
}

// RelaunchAsAdmin re-launches the current executable with elevated (Admin) rights
// using the Windows ShellExecute "runas" verb. This triggers the UAC prompt.
// Returns nil on success (the new elevated process started). The caller should os.Exit(0).
func RelaunchAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get exe path: %w", err)
	}

	// Use cmd.exe to relaunch with runas
	cmd := exec.Command("cmd", "/C", "start", "", "/wait",
		"powershell", "-NoProfile", "-NonInteractive",
		"-Command",
		fmt.Sprintf("Start-Process -FilePath '%s' -Verb RunAs", exe))
	cmd.SysProcAttr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("shellexecute runas: %w", err)
	}
	return nil
}
