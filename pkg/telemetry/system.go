// Package telemetry collects system hardware metrics using gopsutil.
// This replaces the Python psutil + subprocess calls with pure Go equivalents.
package telemetry

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// Snapshot holds a point-in-time reading of all system metrics.
type Snapshot struct {
	CPUPercent     float64            `json:"cpu_percent"`
	RAMTotal       uint64             `json:"ram_total_gb"`
	RAMUsedPercent float64            `json:"ram_used_percent"`
	DiskTotal      uint64             `json:"disk_total_gb"`
	DiskUsedPercent float64           `json:"disk_used_percent"`
	IPAddress      string             `json:"ip_address"`
	OSInfo         string             `json:"os_info"`
	GPUName        string             `json:"gpu_name"`
	Hostname       string             `json:"hostname"`
	Uptime         uint64             `json:"uptime_seconds"`
	Specs          map[string]interface{} `json:"specs"`
	Timestamp      time.Time          `json:"timestamp"`
}

// Collect gathers all hardware metrics and returns a Snapshot.
func Collect() (*Snapshot, error) {
	s := &Snapshot{Timestamp: time.Now().UTC()}

	// CPU (average over 1 second interval — mirrors Python psutil.cpu_percent(interval=1))
	cpuPercents, err := cpu.Percent(1*time.Second, false)
	if err == nil && len(cpuPercents) > 0 {
		s.CPUPercent = cpuPercents[0]
	}

	// RAM
	vmStat, err := mem.VirtualMemory()
	if err == nil {
		s.RAMTotal = vmStat.Total / 1024 / 1024 / 1024 // bytes -> GB
		s.RAMUsedPercent = vmStat.UsedPercent
	}

	// Disk (C:\ on Windows)
	diskPath := "C:\\"
	if runtime.GOOS != "windows" {
		diskPath = "/"
	}
	diskStat, err := disk.Usage(diskPath)
	if err == nil {
		s.DiskTotal = diskStat.Total / 1024 / 1024 / 1024
		s.DiskUsedPercent = diskStat.UsedPercent
	}

	// Network IP
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if strings.Contains(strings.ToLower(iface.Name), "loopback") {
			continue
		}
		for _, addr := range iface.Addrs {
			ip := addr.Addr
			if strings.Contains(ip, ".") && !strings.HasPrefix(ip, "127.") {
				// Strip CIDR notation
				if idx := strings.Index(ip, "/"); idx != -1 {
					ip = ip[:idx]
				}
				s.IPAddress = ip
				break
			}
		}
		if s.IPAddress != "" {
			break
		}
	}

	// OS Info
	hostInfo, _ := host.Info()
	if hostInfo != nil {
		s.OSInfo = fmt.Sprintf("%s %s (Build %s)", hostInfo.Platform, hostInfo.PlatformVersion, hostInfo.KernelVersion)
		s.Hostname = hostInfo.Hostname
		s.Uptime = hostInfo.Uptime
	}

	// GPU Name (Windows WMI)
	s.GPUName = getGPUName()

	// CPU model name
	cpuInfos, _ := cpu.Info()
	cpuModel := "Unknown CPU"
	if len(cpuInfos) > 0 {
		cpuModel = cpuInfos[0].ModelName
	}

	// Specs map (sent in heartbeat and stored in DB)
	s.Specs = map[string]interface{}{
		"cpu":      cpuModel,
		"gpu":      s.GPUName,
		"ram_gb":   s.RAMTotal,
		"disk_gb":  s.DiskTotal,
		"os":       s.OSInfo,
		"hostname": s.Hostname,
	}

	return s, nil
}

// getGPUName queries the GPU name from WMI on Windows.
// Returns "Unknown" if query fails.
func getGPUName() string {
	if runtime.GOOS != "windows" {
		return "N/A"
	}
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-WMIObject -Class Win32_VideoController | Select-Object -First 1).Name")
	out, err := cmd.Output()
	if err != nil {
		return "Unknown"
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "Unknown"
	}
	return name
}
