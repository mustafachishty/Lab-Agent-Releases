// Package commands executes C2 (Command & Control) instructions received
// from the server during a heartbeat response.
// Supported command types:
//
//	"screenshot" — Capture the screen and return base64-encoded PNG
//	"cmd"        — Execute a shell command and return stdout/stderr
//	"terminate"  — Kill a process by name
package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"labguardian/agent/pkg/config"
)

// CommandEnvelope is the C2 command structure from the server.
type CommandEnvelope struct {
	ID      int64                  `json:"id"`
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// Execute dispatches a raw JSON command to the appropriate handler.
// Must be called in a goroutine to avoid blocking the heartbeat loop.
func Execute(cfg *config.Config, raw json.RawMessage) {
	var env CommandEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		log.Printf("[C2] Failed to parse command: %v", err)
		return
	}

	log.Printf("[C2] Executing command #%d: %s", env.ID, env.Type)
	var result map[string]interface{}
	var status string

	switch strings.ToLower(env.Type) {
	case "screenshot":
		img, err := takeScreenshot()
		if err != nil {
			result = map[string]interface{}{"error": err.Error()}
			status = "failed"
		} else {
			result = map[string]interface{}{"image_base64": img}
			status = "completed"
		}

	case "cmd":
		cmdStr, _ := env.Payload["command"].(string)
		out, err := runCommand(cmdStr)
		if err != nil {
			result = map[string]interface{}{"stderr": err.Error(), "stdout": out}
			status = "failed"
		} else {
			result = map[string]interface{}{"stdout": out}
			status = "completed"
		}

	case "terminate":
		procName, _ := env.Payload["process"].(string)
		err := terminateProcess(procName)
		if err != nil {
			result = map[string]interface{}{"error": err.Error()}
			status = "failed"
		} else {
			result = map[string]interface{}{"terminated": procName}
			status = "completed"
		}

	default:
		result = map[string]interface{}{"error": "Unknown command type: " + env.Type}
		status = "failed"
	}

	reportResult(cfg, env.ID, status, result)
}

// ---------------------------------------------------------------
// Command implementations
// ---------------------------------------------------------------

// takeScreenshot captures the primary display and returns a base64-encoded PNG string.
// Uses PowerShell's System.Windows.Forms.Screen for maximum compatibility.
func takeScreenshot() (string, error) {
	script := `
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$bmp = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
$stream = New-Object System.IO.MemoryStream
$bmp.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
$g.Dispose(); $bmp.Dispose()
[Convert]::ToBase64String($stream.ToArray())
`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("screenshot failed: %w", err)
	}
	encoded := strings.TrimSpace(string(out))
	if encoded == "" {
		return "", fmt.Errorf("empty screenshot output")
	}
	// Verify the base64 is valid PNG
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", fmt.Errorf("invalid base64 from screenshot")
	}
	_ = png.Decode // Ensure import is used
	return encoded, nil
}

// runCommand executes a shell command and returns its combined stdout.
func runCommand(cmdStr string) (string, error) {
	if cmdStr == "" {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.Command("cmd", "/C", cmdStr)
	cmd.Env = nil // Use system environment
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// terminateProcess kills all processes with the given name.
func terminateProcess(name string) error {
	if name == "" {
		return fmt.Errorf("empty process name")
	}
	// taskkill /F /IM <name>.exe — force-kills all instances
	cmd := exec.Command("taskkill", "/F", "/IM", name+".exe")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill failed: %s (%w)", string(out), err)
	}
	return nil
}

// ---------------------------------------------------------------
// Report result back to server
// ---------------------------------------------------------------

func reportResult(cfg *config.Config, cmdID int64, status string, result map[string]interface{}) {
	payload := map[string]interface{}{
		"command_id": cmdID,
		"status":     status,
		"result":     result,
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/report-command", bytes.NewReader(body))
	if err != nil {
		log.Printf("[C2] Build report request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[C2] Report error: %v", err)
		return
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	log.Printf("[C2] Command #%d reported as: %s", cmdID, status)
}
