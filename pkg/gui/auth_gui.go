package gui

import (
	"log"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/config"
)

// ShowAuthDialog mimics the original Python auth logic.
// It displays a simple window locking the UI until the HWID is verified against Supabase.
func ShowAuthDialog(cfg *config.Config) bool {
	var mw *walk.MainWindow
	var lblMsg *walk.Label
	
	authenticated := false

	// Test authentication immediately (if it fails, we show the dialog)
	if resp, err := auth.Authenticate(cfg); err == nil && resp != nil && resp.Status == "authorized" {
		return true // already authorized, skip dialog
	}

	err := MainWindow{
		AssignTo: &mw,
		Title:    "Lab Guardian Pro | Agent Authentication",
		MinSize:  Size{Width: 500, Height: 300},
		Size:     Size{Width: 500, Height: 300},
		Layout:   VBox{Margins: Margins{20, 20, 20, 20}, Spacing: 15},
		Background: SolidColorBrush{Color: walk.RGB(15, 15, 15)},
		Children: []Widget{
			Label{
				Text:      "LAB GUARDIAN PRO",
				Font:      Font{Family: "Segoe UI Black", PointSize: 24, Bold: true},
				TextColor: walk.RGB(0, 255, 255),
				Alignment: AlignHCenterVNear,
			},
			Label{
				Text:      "PROFESSIONAL SECURITY PORTAL v" + config.AgentVersion,
				Font:      Font{Family: "Segoe UI", PointSize: 10},
				TextColor: walk.RGB(150, 150, 150),
				Alignment: AlignHCenterVNear,
			},
			VSpacer{Size: 20},
			Label{
				Text:      "? Uplink Requesting: " + cfg.ServerURL + "/api/auth",
				Font:      Font{Family: "Consolas", PointSize: 9, Italic: true},
				TextColor: walk.RGB(0, 200, 150),
				Alignment: AlignHCenterVNear,
			},
			VSpacer{Size: 10},
			Label{
				Text:      "HID: " + cfg.HardwareID,
				Font:      Font{Family: "Segoe UI", PointSize: 10},
				TextColor: walk.RGB(220, 220, 220),
				Alignment: AlignHCenterVNear,
			},
			PushButton{
				Text: "Copy HID",
				Font: Font{PointSize: 10},
				OnClicked: func() {
					walk.Clipboard().SetText(cfg.HardwareID)
				},
			},
			VSpacer{Size: 15},
			Label{
				AssignTo:  &lblMsg,
				Text:      "",
				Font:      Font{PointSize: 9, Bold: true},
				TextColor: walk.RGB(255, 100, 100),
				Alignment: AlignHCenterVNear,
			},
			PushButton{
				Text: "Authenticate System",
				Font: Font{PointSize: 12, Bold: true},
				MinSize: Size{Height: 40},
				OnClicked: func() {
					lblMsg.SetText("Verifying...")
					go func() {
						resp, err := auth.Authenticate(cfg)
						mw.Synchronize(func() {
							if err != nil {
								lblMsg.SetText("Error: Server Offline or Unreachable")
							} else if resp != nil && resp.Status == "authorized" {
								authenticated = true
								mw.Close()
							} else {
								lblMsg.SetText("Access Denied: HID not found in database.")
							}
						})
					}()
				},
			},
		},
	}.Create()

	if err != nil {
		log.Printf("[AUTH GUI] Creation error: %v", err)
		return false
	}

	mw.Run()
	return authenticated
}
