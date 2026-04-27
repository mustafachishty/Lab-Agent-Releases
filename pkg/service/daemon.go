package service

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unsafe"

	"labguardian/agent/pkg/config"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var (
	modadvapi32              = windows.NewLazySystemDLL("advapi32.dll")
	procCheckTokenMembership = modadvapi32.NewProc("CheckTokenMembership")
)

func IsElevated() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	var isAdmin uint32
	ret, _, _ := procCheckTokenMembership.Call(0, uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&isAdmin)))
	return ret != 0 && isAdmin != 0
}

func RelaunchAsAdmin() error {
	verb := "runas"
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	args := strings.Join(os.Args[1:], " ") + " --elevated"

	verbPtr, _ := windows.UTF16PtrFromString(verb)
	exePtr, _ := windows.UTF16PtrFromString(exe)
	cwdPtr, _ := windows.UTF16PtrFromString(cwd)
	argPtr, _ := windows.UTF16PtrFromString(args)

	var showCmd int32 = 1 // SW_SHOWNORMAL

	return windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, showCmd)
}

const ServiceName = "LabGuardianAgent"

type LabGuardianService struct {
	Runner func() // The main logic function (Heartbeat loop)
	Stopper func() // The graceful shutdown function
}

func (m *LabGuardianService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	// Start the main logic in a goroutine
	go m.Runner()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

loop:
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Printf("[SERVICE] Stop requested.")
				if m.Stopper != nil {
					m.Stopper()
				}
				break loop
			default:
				log.Printf("[SERVICE] Unexpected control request #%d", c.Cmd)
			}
		}
	}

	changes <- svc.Status{State: svc.StopPending}
	return
}

func Run(runner func(), stopper func()) {
	err := svc.Run(config.ServiceName, &LabGuardianService{Runner: runner, Stopper: stopper})
	if err != nil {
		log.Fatalf("[SERVICE] Run failed: %v", err)
	}
}

func InstallService() error {
	exepath, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// 1. Cleanup existing service if it exists
	s, err := m.OpenService(config.ServiceName)
	if err == nil {
		defer s.Close()
		log.Printf("[SERVICE] Existing service found. Cleaning up...")
		
		// Stop if running
		st, qerr := s.Query()
		if qerr == nil && st.State != svc.Stopped {
			_, _ = s.Control(svc.Stop)
			// Wait a bit for stop
			for i := 0; i < 10; i++ {
				time.Sleep(200 * time.Millisecond)
				if st, qerr = s.Query(); qerr == nil && st.State == svc.Stopped {
					break
				}
			}
		}

		// Delete
		err = s.Delete()
		if err != nil {
			log.Printf("[SERVICE] Warning: Failed to delete existing service: %v", err)
		} else {
			log.Printf("[SERVICE] Existing service deleted.")
			s.Close()
			time.Sleep(500 * time.Millisecond) // Give SCM time to process
		}
	}

	// 2. Create and start fresh service
	s, err = m.CreateService(config.ServiceName, exepath, mgr.Config{
		DisplayName: "Lab Guardian Agent",
		Description: "Monitors lab system usage and health.",
		StartType:   mgr.StartAutomatic,
	}, "--service")
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	log.Printf("[SERVICE] Fresh service created. Starting...")
	return s.Start()
}

func RemoveService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(config.ServiceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", config.ServiceName)
	}
	defer s.Close()

	if st, qerr := s.Query(); qerr == nil && st.State == svc.Running {
		_, _ = s.Control(svc.Stop)
		for i := 0; i < 15; i++ {
			time.Sleep(200 * time.Millisecond)
			if st, qerr = s.Query(); qerr == nil && st.State == svc.Stopped {
				break
			}
		}
	}

	err = s.Delete()
	if err != nil {
		return err
	}
	return nil
}

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
