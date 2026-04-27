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
	var dlg *walk.Dialog
	var lblMsg *walk.Label
	
	authenticated := false

	// Test authentication immediately (if it fails, we show the dialog)
	if resp, err := auth.Authenticate(cfg); err == nil && resp != nil && resp.Status == "authorized" {
		return true // already authorized, skip dialog
	}

	code, err := Dialog{
		AssignTo: &dlg,
		Title:    "Lab Guardian Pro | Agent Authentication",
		MinSize:  Size{Width: 500, Height: 300},
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
				Font:      Font{Family: "Consolas", PointSize: 12, Bold: true},
				TextColor: walk.RGB(255, 255, 0), // Bright yellow for visibility
				Alignment: AlignHCenterVNear,
			},
			PushButton{
				Text: "Copy Hardware ID (Check Supabase Match)",
				Font: Font{PointSize: 10},
				OnClicked: func() {
					walk.Clipboard().SetText(cfg.HardwareID)
				},
			},
			VSpacer{Size: 15},
			Label{
				AssignTo:  &lblMsg,
				Text:      "System Standby - Verification Required",
				Font:      Font{PointSize: 10, Bold: true},
				TextColor: walk.RGB(150, 150, 150),
				Alignment: AlignHCenterVNear,
			},
			PushButton{
				Text: "Authenticate System",
				Font: Font{PointSize: 12, Bold: true},
				MinSize: Size{Height: 45},
				OnClicked: func() {
					lblMsg.SetText("Verifying...")
					lblMsg.SetTextColor(walk.RGB(200, 200, 0))
					go func() {
						resp, err := auth.Authenticate(cfg)
						dlg.Synchronize(func() {
							if err != nil {
								lblMsg.SetText("Error: Server Offline or API Timeout")
								lblMsg.SetTextColor(walk.RGB(255, 50, 50))
							} else if resp != nil && resp.Status == "authorized" {
								authenticated = true
								dlg.Accept()
							} else {
								lblMsg.SetText("Access Denied: HID NOT FOUND! Check for typos in Supabase.")
								lblMsg.SetTextColor(walk.RGB(255, 50, 50))
							}
						})
					}()
				},
			},
		},
	}.Run(nil)
	_ = code

	if err != nil && err != walk.ErrInvalidType {
		log.Printf("[AUTH GUI] Dialog error: %v", err)
		return false
	}

	return authenticated
}

func ShowFatalError(title string, err error) {
	walk.MsgBox(nil, "Lab Guardian - FATAL ERROR", title+":\n"+err.Error(), walk.MsgBoxIconError)
}
