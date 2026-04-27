package gui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"labguardian/agent/pkg/auth"
	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/persistence"
	"labguardian/agent/pkg/service"
)

type SlotRow struct {
	SystemID string `json:"system_id"`
	District string `json:"city"`
	Tehsil   string `json:"tehsil"`
	LabName  string `json:"lab_name"`
}

func fetchAvailable(url string) ([]SlotRow, error) {
	resp, err := http.Get(url + "/api/available-systems")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var slots []SlotRow
	if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
		return nil, err
	}
	return slots, nil
}

func bindSystemGui(serverURL, hid, sysID, city, tehsil, labName string) error {
	payload := map[string]string{
		"hardware_id": hid,
		"system_id":   sysID,
		"city":        city,
		"tehsil":      tehsil,
		"lab_name":    labName,
	}
	if name, err := os.Hostname(); err == nil {
		payload["pc_name"] = name
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(serverURL+"/api/bind", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

//go:embed hierarchy.json
var hierarchyBytes []byte

func loadHierarchy() map[string]map[string][]string {
	var h map[string]map[string][]string
	if err := json.Unmarshal(hierarchyBytes, &h); err != nil {
		return map[string]map[string][]string{
			"Error": {"None": {"Invalid embedded hierarchy.json format"}},
		}
	}
	return h
}

func RunGUI(cfg *config.Config) {
	var mw *walk.MainWindow
	var serverURL *walk.LineEdit
	var cbDistrict, cbTehsil, cbLab, cbSlot *walk.ComboBox
	var outText, trackerText *walk.TextEdit
	var lblServiceStatus, lblNetworkStatus, lblRegistration *walk.Label
	var compositeMonitoring *walk.Composite

	var availableSlots []SlotRow
	var hierarchy = loadHierarchy()

	var allDistricts []string
	for k := range hierarchy {
		allDistricts = append(allDistricts, k)
	}
	sort.Strings(allDistricts)

	updateCombo := func(cb *walk.ComboBox, items []string) {
		sort.Strings(items)
		_ = cb.SetModel(items)
		_ = cb.SetCurrentIndex(-1)
	}

	appendLog := func(msg string) {
		t := time.Now().Format("15:04:05")
		if mw != nil {
			mw.Synchronize(func() {
				if outText != nil {
					outText.AppendText(fmt.Sprintf("[%s] %s\r\n", t, msg))
				}
			})
		}
	}

	// 🎨 UI Constants
	green := walk.RGB(0, 160, 0)
	red := walk.RGB(200, 0, 0)
	darkBlue := walk.RGB(0, 32, 96)

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "Lab Guardian Pro - Professional Management Console",
		MinSize:  Size{Width: 1050, Height: 700},
		Layout:   HBox{SpacingZero: true, MarginsZero: true},
		Children: []Widget{
			// LEFT SIDEBAR
			Composite{
				Layout:  VBox{MarginsZero: false, Spacing: 5},
				MinSize: Size{Width: 240, Height: 0},
				MaxSize: Size{Width: 240, Height: 0},
				Background: SolidColorBrush{Color: walk.RGB(245, 245, 245)},
				Children: []Widget{
					Label{
						Text: "LAB GUARDIAN PRO",
						Font: Font{Family: "Segoe UI", PointSize: 14, Bold: true},
						TextColor: darkBlue,
					},
					Label{
						Text: "Agent v" + config.AgentVersion,
						Font: Font{PointSize: 8, Italic: true},
					},
					VSpacer{Size: 20},
					GroupBox{
						Title: "DEVICE IDENTITY",
						Layout: VBox{Margins: Margins{5,5,5,5}},
						Children: []Widget{
							Label{
								Text: "HWID: " + cfg.HardwareID,
								Font: Font{PointSize: 7},
							},
						},
					},
					VSpacer{Size: 10},
					PushButton{
						Text: "Install Background Service",
						OnClicked: func() {
							appendLog("Requesting SCM connection for installation...")
							if err := service.InstallService(); err != nil {
								walk.MsgBox(mw, "Error", "Failed to install service: "+err.Error(), walk.MsgBoxIconError)
							} else {
								walk.MsgBox(mw, "Success", "Background service installed and started.", walk.MsgBoxIconInformation)
							}
						},
					},
					PushButton{
						Text: "Remove Background Tracking",
						OnClicked: func() {
							appendLog("Requesting service removal...")
							if err := service.RemoveService(); err != nil {
								walk.MsgBox(mw, "Error", "Removal failed: "+err.Error(), walk.MsgBoxIconError)
							} else {
								walk.MsgBox(mw, "Success", "Service removed.", walk.MsgBoxIconInformation)
							}
						},
					},
					VSpacer{Size: 25},
					// STATUS PANEL
					GroupBox{
						Title: "LIVE CONNECTIVITY",
						Layout: VBox{Margins: Margins{10,10,10,10}, Spacing: 10},
						Children: []Widget{
							Label{
								AssignTo: &lblNetworkStatus,
								Text:     "📶 NETWORK: PROBING...",
								Font:     Font{Bold: true},
							},
							Label{
								AssignTo: &lblServiceStatus,
								Text:     "⚙️ SERVICE: CHECKING...",
								Font:     Font{Bold: true},
							},
							Label{
								AssignTo: &lblRegistration,
								Text:     "🔑 STATUS: CHECKING...",
								Font:     Font{Bold: true},
							},
						},
					},
					VSpacer{},
				},
			},
			// MAIN WORK AREA
			Composite{
				Layout: VBox{MarginsZero: false, Spacing: 10},
				Children: []Widget{
					// REGISTRATION TOOLBAR
					GroupBox{
						Title:  "PC Registration & Lab Assignment",
						Layout: Grid{Columns: 2},
						Children: []Widget{
							Label{Text: "City/District", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbDistrict},
							
							Label{Text: "Tehsil/Town", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbTehsil},
							
							Label{Text: "Lab Name", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbLab},

							Label{Text: "API Server URL", MinSize: Size{Width: 100}},
							LineEdit{AssignTo: &serverURL, Text: cfg.ServerURL},
							
							Label{Text: "Selected Slot ID", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbSlot},
							
							Composite{
								ColumnSpan: 2,
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									PushButton{
										Text: "Scan for Free Slots",
										OnClicked: func() {
											u := serverURL.Text()
											if u == "" { return }
											appendLog("Scanning server for available PC slots...")
											slots, err := fetchAvailable(u)
											if err != nil {
												appendLog(fmt.Sprintf("Scan failed: %v", err))
												return
											}
											availableSlots = slots
											
											// Include current slot if it matches the current PC
											currentFound := false
											for _, s := range availableSlots {
												if s.SystemID == cfg.SystemID {
													currentFound = true
													break
												}
											}
											if !currentFound && cfg.SystemID != "" {
												availableSlots = append(availableSlots, SlotRow{
													SystemID: cfg.SystemID,
													District: cfg.District,
													Tehsil:   cfg.Tehsil,
													LabName:  cfg.LabName,
												})
											}

											d, l := cbDistrict.Text(), cbLab.Text()
											var list []string
											for _, s := range availableSlots {
												if (s.District == d && s.LabName == l) || s.LabName == "" || s.LabName == "Unknown" || s.SystemID == cfg.SystemID {
													list = append(list, s.SystemID)
												}
											}
											updateCombo(cbSlot, list)
											if cfg.SystemID != "" {
												cbSlot.SetText(cfg.SystemID)
											}
											appendLog(fmt.Sprintf("Found %d slots (including yours) for selection.", len(list)))
										},
									},
									PushButton{
										Text: "Save & Update Info",
										Font: Font{Bold: true},
										OnClicked: func() {
											slot := cbSlot.Text()
											if slot == "" { return }
											appendLog("Sending binding request to central server...")
											hid, _ := auth.GetHardwareID()
											err := bindSystemGui(serverURL.Text(), hid, slot, cbDistrict.Text(), cbTehsil.Text(), cbLab.Text())
											if err != nil {
												appendLog(fmt.Sprintf("Request REJECTED: %v", err))
												return
											}
											cfg.SystemID = slot
											cfg.District = cbDistrict.Text()
											cfg.Tehsil = cbTehsil.Text()
											cfg.LabName = cbLab.Text()
											
											resp, _ := auth.Authenticate(cfg)
											if resp != nil && resp.Status == "authorized" {
												cfg.AuthToken = resp.Token
											}

											persistence.SetConfig("auth_token", cfg.AuthToken)
											
											errs := []error{
												persistence.SetConfig("system_id", cfg.SystemID),
												persistence.SetConfig("city", cfg.District),
												persistence.SetConfig("tehsil", cfg.Tehsil),
												persistence.SetConfig("lab_name", cfg.LabName),
											}
											
											failed := false
											for _, e := range errs {
												if e != nil {
													failed = true
													appendLog(fmt.Sprintf("LOCAL SAVE FAILED: %v", e))
												}
											}

											if failed {
												walk.MsgBox(mw, "Database Error", "The information was sent to the server, but could not be saved locally.\nCheck folder permissions for C:\\ProgramData\\LabGuardian.", walk.MsgBoxIconWarning)
											} else {
												appendLog("SUCCESS: Information saved and synchronized.")
												walk.MsgBox(mw, "Success", "Configuration updated successfully.", walk.MsgBoxIconInformation)
											}
										},
									},
								},
							},
						},
					},
					// MONITORING SUITE
					Composite{
						AssignTo: &compositeMonitoring,
						Layout:   VBox{MarginsZero: true},
						Children: []Widget{
							Label{
								Text: "ACTIVE MONITORING SUITE",
								Font: Font{Bold: true, PointSize: 10},
								TextColor: darkBlue,
							},
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									TextEdit{
										AssignTo: &outText,
										ReadOnly: true,
										VScroll:  true,
										Font:     Font{Family: "Consolas", PointSize: 9},
										Text:     "Initial check complete. Waiting for server heartbeat...\r\n",
									},
									TextEdit{
										AssignTo: &trackerText,
										ReadOnly: true,
										MinSize:  Size{Width: 380},
										VScroll:  true,
										Font:     Font{Family: "Consolas", PointSize: 9},
										Text:     "Application tracker standby.\r\n",
									},
								},
							},
						},
					},
				},
			},
		},
	}).Create(); err != nil {
		log.Fatal(err)
	}

	// 🔄 DATA BINDING & REFRESH LOGIC
	cbDistrict.CurrentIndexChanged().Attach(func() {
		d := cbDistrict.Text()
		var ts []string
		if tm, ok := hierarchy[d]; ok {
			for k := range tm { ts = append(ts, k) }
		}
		updateCombo(cbTehsil, ts)
	})
	cbTehsil.CurrentIndexChanged().Attach(func() {
		d, t := cbDistrict.Text(), cbTehsil.Text()
		if tehsils, ok := hierarchy[d]; ok {
			if labs, ok := tehsils[t]; ok {
				updateCombo(cbLab, labs)
				return
			}
		}
		updateCombo(cbLab, []string{})
	})

	updateCombo(cbDistrict, allDistricts)
	if cfg.District != "" {
		_ = cbDistrict.SetText(cfg.District)
		// Trigger hierarchy population manually
		if tm, ok := hierarchy[cfg.District]; ok {
			var ts []string
			for k := range tm { ts = append(ts, k) }
			updateCombo(cbTehsil, ts)
			if cfg.Tehsil != "" {
				_ = cbTehsil.SetText(cfg.Tehsil)
				if labs, ok := tm[cfg.Tehsil]; ok {
					updateCombo(cbLab, labs)
					if cfg.LabName != "" { 
						_ = cbLab.SetText(cfg.LabName) 
					}
				}
			}
		}
	}
	
	// Ensure slot is selectable even before scan
	if cfg.SystemID != "" {
		updateCombo(cbSlot, []string{cfg.SystemID})
		_ = cbSlot.SetText(cfg.SystemID)
	}

	// 🕵️ LIVE STATUS LOOP
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			isOnline := false
			resp, err := client.Get(cfg.ServerURL + "/api/health")
			if err == nil {
				isOnline = (resp.StatusCode == 200)
				resp.Body.Close()
			}

			running := service.IsRunning()
			registered := (cfg.SystemID != "")

			mw.Synchronize(func() {
				if isOnline {
					lblNetworkStatus.SetText("🌐 NETWORK: ONLINE")
					lblNetworkStatus.SetTextColor(green)
				} else {
					lblNetworkStatus.SetText("🌐 NETWORK: OFFLINE")
					lblNetworkStatus.SetTextColor(red)
				}

				if running {
					lblServiceStatus.SetText("🚀 SERVICE: RUNNING")
					lblServiceStatus.SetTextColor(green)
				} else {
					lblServiceStatus.SetText("🛑 SERVICE: STOPPED")
					lblServiceStatus.SetTextColor(red)
				}

				if registered {
					lblRegistration.SetText("✅ REGISTERED [" + cfg.SystemID + "]")
					lblRegistration.SetTextColor(green)
					compositeMonitoring.SetEnabled(true)
				} else {
					lblRegistration.SetText("❌ STATUS: UNREGISTERED")
					lblRegistration.SetTextColor(red)
					compositeMonitoring.SetEnabled(false)
					trackerText.SetText("MONITORING HALTED.")
				}
			})
			time.Sleep(4 * time.Second)
		}
	}()

	// 📈 APP TRACKER LOOP
	go func() {
		for {
			time.Sleep(3 * time.Second)
			if cfg.SystemID == "" { continue }

			dataStr := persistence.GetConfig("metrics_cache")
			if dataStr == "" { continue }
			var apps map[string]int
			if err := json.Unmarshal([]byte(dataStr), &apps); err != nil { continue }
			type appStat struct { n string; s int }
			var st []appStat
			for k, v := range apps { if k != "__current_cpu__" { st = append(st, appStat{k, v}) } }
			sort.Slice(st, func(i, j int) bool { return st[i].s > st[j].s })
			var b strings.Builder
			b.WriteString("TOP ACTIVE APPLICATIONS:\r\n========================\r\n")
			for i := 0; i < 8 && i < len(st); i++ {
				b.WriteString(fmt.Sprintf("[%d] %-20s -> %dm %ds\r\n", i+1, st[i].n, st[i].s/60, st[i].s%60))
			}
			mw.Synchronize(func() { trackerText.SetText(b.String()) })
		}
	}()

	mw.Run()
}
