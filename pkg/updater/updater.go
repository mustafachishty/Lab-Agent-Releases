//go:build windows

// Package updater implements the OTA (Over-The-Air) self-update mechanism.
// When the server reports a newer version, the agent:
//  1. Downloads the new binary from /api/update-payload
//  2. Saves it as agent_new.exe beside the running binary
//  3. Writes a small updater.bat that stops the service, renames files, restarts
//  4. Launches the bat and then exits — the bat takes over
//
// This approach ensures zero-downtime on managed machines and requires
// no external dependencies beyond the standard Windows command prompt.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"labguardian/agent/pkg/config"
)

// GithubRepo is the GitHub repository path used for OTA updates.
// The agent downloads the latest release .exe from this repo automatically.
// Releases URL: https://github.com/mustafachishty/Lab-Agent-Releases/releases
const GithubRepo = "mustafachishty/Lab-Agent-Releases"
const GithubAPI  = "https://api.github.com/repos/" + GithubRepo + "/releases/latest"


func Update(cfg *config.Config, newVersion string, targetHash string) {
	// FINAL SAFETY CHECK
	if newVersion == config.AgentVersion {
		return
	}

	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	newExePath := filepath.Join(exeDir, "agent_new.exe")
	psPath := filepath.Join(exeDir, "updater.ps1")

	log.Printf("[UPDATER] Initiating province-ready update: %s -> %s", config.AgentVersion, newVersion)

	// Enhancement 1: Resumable Download
	if err := downloadPayloadResumable(newExePath); err != nil {
		log.Printf("[UPDATER] Download failed: %v", err)
		return
	}

	// Enhancement 2: Integrity Check
	// Note: We will implement the server-side hash delivery in the next step
	log.Println("[UPDATER] Verifying download integrity...")
	if err := verifyFile(newExePath, targetHash); err != nil {
		log.Printf("[UPDATER] Integrity check failed: %v", err)
		_ = os.Remove(newExePath)
		return
	}

	// Enhancement 3: Smart PowerShell Swap
	psContent := fmt.Sprintf(`
$serviceName = "%s"
$exePath = "%s"
$newExe = "%s"
$logFile = Join-Path (Split-Path $exePath) "update_log.txt"

Function Log($msg) {
    "$(Get-Date -Format 'HH:mm:ss') $msg" | Out-File -FilePath $logFile -Append
}

Log "Starting smart swap sequence..."
Stop-Service $serviceName -ErrorAction SilentlyContinue

# Wait for process to exit (intelligent wait)
$timeout = 20
$timer = [diagnostics.stopwatch]::StartNew()
while ((Get-Process "agent" -ErrorAction SilentlyContinue) -and ($timer.Elapsed.TotalSeconds -lt $timeout)) {
    Log "Waiting for agent process to terminate..."
    Start-Sleep -Seconds 1
}

# Force kill if still running
Get-Process "agent" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# Perform move with retries
$success = $false
for ($i=1; $i -le 5; $i++) {
    try {
        Move-Item -Path $newExe -Destination $exePath -Force -ErrorAction Stop
        $success = $true
        Log "Successfully replaced binary."
        break
    } catch {
        Log "Move attempt $i failed, retrying..."
        Start-Sleep -Seconds 2
    }
}

if ($success) {
    Log "Starting service..."
    Start-Service $serviceName
}
Log "Update complete."
`, config.ServiceName, exePath, newExePath)

	_ = os.WriteFile(psPath, []byte(psContent), 0o755)

	log.Println("[UPDATER] Launching PowerShell swap. Monitoring system will restart in 30 seconds.")
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", psPath)
	cmd.Start()
	os.Exit(0)
}

func downloadPayloadResumable(dest string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", GithubAPI, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	json.NewDecoder(resp.Body).Decode(&release)

	var downloadURL string
	for _, a := range release.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".exe") {
			downloadURL = a.URL
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no exe found in assets")
	}

	// Check existing file size for "Range" resume
	var startPos int64 = 0
	if info, err := os.Stat(dest); err == nil {
		startPos = info.Size()
	}

	fileReq, _ := http.NewRequest("GET", downloadURL, nil)
	if startPos > 0 {
		fileReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", startPos))
		log.Printf("[UPDATER] Attempting resume from %d bytes...", startPos)
	}

	// PRO-TIP: We need a custom CheckRedirect to preserve the "Range" header 
	// because GitHub redirects to S3 and Go strips headers on cross-domain redirects.
	fileClient := &http.Client{
		Timeout: 15 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Copy the Range header to the new request
			if rangeHeader := via[0].Header.Get("Range"); rangeHeader != "" {
				req.Header.Set("Range", rangeHeader)
			}
			return nil
		},
	}

	fileResp, err := fileClient.Do(fileReq)
	if err != nil {
		return err
	}
	defer fileResp.Body.Close()

	var out *os.File
	if fileResp.StatusCode == http.StatusPartialContent {
		log.Printf("[UPDATER] Server accepted Range request. Appending to existing file.")
		out, err = os.OpenFile(dest, os.O_APPEND|os.O_WRONLY, 0o644)
	} else {
		if startPos > 0 {
			log.Printf("[UPDATER] WARNING: Server returned %d (expected 206). Resuming failed, starting from zero.", fileResp.StatusCode)
		}
		out, err = os.Create(dest)
	}

	if err != nil {
		return err
	}
	defer out.Close()

	written, err := io.Copy(out, fileResp.Body)
	if err != nil {
		return err
	}

	log.Printf("[UPDATER] Download chunk finished (%d bytes). New file size: %d", written, startPos+written)
	return nil
}

func verifyFile(path string, targetHash string) error {
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
	log.Printf("[INTEGRITY] Computed SHA-256: %s", actual)

	// Province-Ready verification: Only fail if targetHash is actually provided and non-zero
	if targetHash != "" && targetHash != "unknown" && targetHash != "0" {
		if strings.ToLower(actual) != strings.ToLower(targetHash) {
			return fmt.Errorf("hash mismatch: expected %s, got %s", targetHash, actual)
		}
		log.Println("[INTEGRITY] Hash verified successfully. Proceeding with installation.")
	} else {
		log.Println("[INTEGRITY] WARNING: No target hash provided by server. Skipping strict verification (not recommended for production).")
	}
	return nil
}
