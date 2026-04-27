package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"labguardian/agent/pkg/config"
)

func Update(cfg *config.Config, newVersion string, targetHash string, isService bool) {
	if newVersion == config.AgentVersion {
		return
	}

	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	exeName := filepath.Base(exePath)
	partPath := filepath.Join(exeDir, "agent_new.exe.part")
	newExePath := filepath.Join(exeDir, "agent_new.exe")
	batPath := filepath.Join(exeDir, "swap.bat")

	log.Printf("[UPDATER] Downloading %s (isService: %v)...", newVersion, isService)

	// 1. Streaming Download
	if err := downloadFile(cfg.ServerURL+"/api/update-payload", partPath); err != nil {
		log.Printf("[UPDATER] Download failed: %v", err)
		return
	}

	// 2. Hash Verification
	if err := verifyHash(partPath, targetHash); err != nil {
		log.Printf("[UPDATER] Verification failed: %v", err)
		_ = os.Remove(partPath)
		return
	}

	_ = os.Remove(newExePath)
	if err := os.Rename(partPath, newExePath); err != nil {
		log.Printf("[UPDATER] Rename failed: %v", err)
		return
	}

	// 3. Robust Bat Swap
	// We use taskkill to ensure the process is REALLY dead before moving
	restartCmd := fmt.Sprintf("sc start %s", config.ServiceName)
	if !isService {
		restartCmd = fmt.Sprintf("start \"\" \"%s\"", exePath)
	}

	batContent := fmt.Sprintf(`@echo off
echo [SWAP] Waiting for process to exit...
ping 127.0.0.1 -n 3 > nul
taskkill /F /IM "%s" /T > nul 2>&1
ping 127.0.0.1 -n 2 > nul
move /Y "%s" "%s"
if errorlevel 1 (
    echo [ERROR] Move failed. Retrying in 5s...
    ping 127.0.0.1 -n 5 > nul
    move /Y "%s" "%s"
)
%s
del "%%~f0"
`, exeName, newExePath, exePath, newExePath, exePath, restartCmd)

	_ = os.WriteFile(batPath, []byte(batContent), 0o755)

	cmd := exec.Command("cmd", "/C", batPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
	_ = cmd.Start()
	os.Exit(0)
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("update download HTTP status %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func verifyHash(path, target string) error {
	if target == "" || target == "unknown" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != target {
		return fmt.Errorf("hash mismatch")
	}
	return nil
}
