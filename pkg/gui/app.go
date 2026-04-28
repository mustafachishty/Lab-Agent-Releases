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

	"go_lms_agent/pkg/auth"
	"go_lms_agent/pkg/config"
	"go_lms_agent/pkg/service"
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

func loadLocalUsage() map[string]int {
	data, err := os.ReadFile(config.MetricsCacheFile)
	if err != nil {
		return make(map[string]int)
	}
	var apps map[string]int
	if err := json.Unmarshal(data, &apps); err != nil {
		return make(map[string]int)
	}
	return apps
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
		if len(items) > 0 {
			_ = cb.SetCurrentIndex(0)
		} else {
			_ = cb.SetCurrentIndex(-1)
		}
	}

	appendLog := func(msg string) {
		t := time.Now().Format("15:04:05")
		if mw != nil {
			mw.Synchronize(func() {
				outText.AppendText(fmt.Sprintf("[%s] %s\r\n", t, msg))
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
							if err := service.Install(); err != nil {
								appendLog(fmt.Sprintf("Installation failed: %v", err))
								walk.MsgBox(mw, "Error", "Permission Denied. Run as Admin.", walk.MsgBoxIconError)
							} else {
								appendLog("Service installed and activated.")
							}
						},
					},
					PushButton{
						Text: "Remove Background Tracking",
						OnClicked: func() {
							appendLog("Requesting service removal...")
							if err := service.Uninstall(); err != nil {
								appendLog(fmt.Sprintf("Removal failed: %v", err))
							} else {
								appendLog("Service completely removed.")
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
							// ROW 1
							Label{Text: "City/District", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbDistrict},
							
							// ROW 2
							Label{Text: "Tehsil/Town", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbTehsil},
							
							// ROW 3
							Label{Text: "Lab Name", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbLab},

							// ROW 4 (Moved below Lab Name)
							Label{Text: "API Server URL", MinSize: Size{Width: 100}},
							LineEdit{AssignTo: &serverURL, Text: cfg.ServerURL},
							
							// ROW 5 (Moved below API URL)
							Label{Text: "Selected Slot ID", MinSize: Size{Width: 100}},
							ComboBox{AssignTo: &cbSlot},
							
							// ROW 6: ACTIONS
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
											d, l := cbDistrict.Text(), cbLab.Text()
											var list []string
											for _, s := range availableSlots {
												if (s.District == d && s.LabName == l) || s.LabName == "" || s.LabName == "Unknown" {
													list = append(list, s.SystemID)
												}
											}
											updateCombo(cbSlot, list)
											appendLog(fmt.Sprintf("Found %d available slots for selection.", len(list)))
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
											_ = config.Save(cfg)
											appendLog("SUCCESS: Information saved and synchronized.")
											walk.MsgBox(mw, "Success", "Configuration updated successfully.", walk.MsgBoxIconInformation)
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
		if tm, ok := hierarchy[cfg.District]; ok {
			var ts []string
			for k := range tm { ts = append(ts, k) }
			updateCombo(cbTehsil, ts)
			if cfg.Tehsil != "" {
				_ = cbTehsil.SetText(cfg.Tehsil)
				if labs, ok := tm[cfg.Tehsil]; ok {
					updateCombo(cbLab, labs)
					if cfg.LabName != "" { _ = cbLab.SetText(cfg.LabName) }
				}
			}
		}
	}
	if cfg.SystemID != "" {
		updateCombo(cbSlot, []string{cfg.SystemID})
		_ = cbSlot.SetText(cfg.SystemID)
	}

	// 🕵️ LIVE STATUS LOOP (The "Missing Piece")
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
				// Network Label
				if isOnline {
					lblNetworkStatus.SetText("🌐 NETWORK: ONLINE")
					lblNetworkStatus.SetTextColor(green)
				} else {
					lblNetworkStatus.SetText("🌐 NETWORK: OFFLINE")
					lblNetworkStatus.SetTextColor(red)
				}

				// Service Label
				if running {
					lblServiceStatus.SetText("🚀 SERVICE: RUNNING")
					lblServiceStatus.SetTextColor(green)
				} else {
					lblServiceStatus.SetText("🛑 SERVICE: STOPPED")
					lblServiceStatus.SetTextColor(red)
				}

				// Registration Label
				if registered {
					lblRegistration.SetText("✅ REGISTERED [" + cfg.SystemID + "]")
					lblRegistration.SetTextColor(green)
					compositeMonitoring.SetEnabled(true)
				} else {
					lblRegistration.SetText("❌ STATUS: UNREGISTERED")
					lblRegistration.SetTextColor(red)
					compositeMonitoring.SetEnabled(false)
					trackerText.SetText("MONITORING HALTED.\r\nSYSTEM IS UNREGISTERED OR REJECTED FROM SERVER.")
				}
			})
			time.Sleep(4 * time.Second)
		}
	}()

	// 📈 APP TRACKER LOOP
	go func() {
		for {
			time.Sleep(3 * time.Second)
			
			// DO NOT DISPLAY ANYTHING IF UNREGISTERED
			if cfg.SystemID == "" {
				continue
			}

			data, err := os.ReadFile(config.MetricsCacheFile)
			if err != nil { continue }
			var apps map[string]int
			if err := json.Unmarshal(data, &apps); err != nil { continue }
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
