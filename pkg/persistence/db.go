package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"labguardian/agent/pkg/config"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func Init() error {
	// 1. Resolve Locks
	ClearLocks()

	// 2. Resolve ProgramData directory
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	dbDir := filepath.Join(programData, "LabGuardian", "data")
	
	// 3. Ensure directory exists with full permissions
	if err := os.MkdirAll(dbDir, 0777); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dbDir, err)
	}

	// 4. Set absolute DB path
	dbPath := filepath.Join(dbDir, "agent_state.db")
	return openDB(dbPath)
}

func ClearLocks() {
	// Dynamically find our own EXE name to kill zombies correctly
	exeName := filepath.Base(os.Args[0])
	if !strings.HasSuffix(strings.ToLower(exeName), ".exe") {
		exeName += ".exe"
	}

	log.Printf("[DB] Clearing locks and stopping existing service instances...")

	// 1. Stop service with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "sc", "stop", config.ServiceName).Run()

	// 2. Kill zombies of the same EXE (but not ourselves)
	myPid := os.Getpid()
	// More efficient way to kill all OTHER instances of the same name
	// We use taskkill's filter to skip our own PID
	filter := fmt.Sprintf("IMAGENAME eq %s", exeName)
	pidFilter := fmt.Sprintf("PID ne %d", myPid)
	
	_ = exec.Command("taskkill", "/F", "/FI", filter, "/FI", pidFilter).Run()
	
	// Brief pause to ensure OS handles the kills
	time.Sleep(300 * time.Millisecond)
}

func openDB(path string) error {
	var err error
	// Build enterprise DSN with WAL and busy_timeout
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", path, err)
	}

	schemas := []string{
		`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS daily_state (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS app_usage (date TEXT, app_name TEXT, duration_secs INTEGER, PRIMARY KEY(date, app_name))`,
		`CREATE TABLE IF NOT EXISTS journal (id INTEGER PRIMARY KEY AUTOINCREMENT, data TEXT, created_at DATETIME)`,
	}

	for _, s := range schemas {
		if _, err := db.Exec(s); err != nil {
			_ = db.Close()
			return fmt.Errorf("apply schema: %w", err)
		}
	}

	// Write probe
	if _, err := db.Exec(`INSERT OR REPLACE INTO daily_state (key, value) VALUES ('__write_probe__', 'ok')`); err != nil {
		_ = db.Close()
		return fmt.Errorf("write probe failed: %w", err)
	}
	return nil
}

func SetConfig(key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, key, value)
	if err != nil {
		log.Printf("[DB ERROR] SetConfig(%s): %v", key, err)
		return err
	}
	return nil
}

func GetConfig(key string) string {
	var val string
	_ = db.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&val)
	return val
}

func GetState(key string) string {
	var val string
	_ = db.QueryRow(`SELECT value FROM daily_state WHERE key = ?`, key).Scan(&val)
	return val
}

func SetState(key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO daily_state (key, value) VALUES (?, ?)`, key, value)
	if err != nil {
		log.Printf("[DB ERROR] SetState(%s): %v", key, err)
		return err
	}
	return nil
}

func ClearDailyData() {
	if _, err := db.Exec(`DELETE FROM daily_state`); err != nil {
		log.Printf("[DB ERROR] ClearDailyData(state): %v", err)
	}
	if _, err := db.Exec(`DELETE FROM app_usage`); err != nil {
		log.Printf("[DB ERROR] ClearDailyData(usage): %v", err)
	}
}

func GetAppUsage(date string) map[string]int {
	rows, err := db.Query(`SELECT app_name, duration_secs FROM app_usage WHERE date = ?`, date)
	if err != nil {
		log.Printf("[DB ERROR] GetAppUsage: %v", err)
		return nil
	}
	defer rows.Close()
	usage := make(map[string]int)
	for rows.Next() {
		var name string
		var secs int
		_ = rows.Scan(&name, &secs)
		usage[name] = secs
	}
	return usage
}

func GetTotalAppUsage() map[string]int {
	now := time.Now().Format("2006-01-02")
	return GetAppUsage(now)
}

func UpdateAppUsage(date string, usage map[string]int) {
	for name, secs := range usage {
		if secs <= 0 {
			continue
		}
		_, err := db.Exec(`
			INSERT INTO app_usage (date, app_name, duration_secs) 
			VALUES (?, ?, ?)
			ON CONFLICT(date, app_name) 
			DO UPDATE SET duration_secs = duration_secs + EXCLUDED.duration_secs`,
			date, name, secs)
		if err != nil {
			log.Printf("[DB ERROR] UpdateAppUsage(%s): %v", name, err)
		}
	}
}

// Journaling
func AddJournal(data string) {
	db.Exec(`INSERT INTO journal (data, created_at) VALUES (?, ?)`, data, time.Now())
}

func GetPendingJournals() ([]struct {
	ID   int
	Data string
}, error) {
	rows, err := db.Query(`SELECT id, data FROM journal ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []struct {
		ID   int
		Data string
	}
	for rows.Next() {
		var r struct {
			ID   int
			Data string
		}
		rows.Scan(&r.ID, &r.Data)
		results = append(results, r)
	}
	return results, nil
}

func DeleteJournal(id int) {
	db.Exec(`DELETE FROM journal WHERE id = ?`, id)
}

func CleanOldJournals(days int) {
	cutoff := time.Now().AddDate(0, 0, -days)
	db.Exec(`DELETE FROM journal WHERE created_at < ?`, cutoff)
}
