package db

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

const dbPath = `C:\ProgramData\LabGuardianAgent\agent.db`

func InitDB() {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("[DB] Failed to create directory: %v", err)
	}

	var err error
	// Open with WAL mode enabled via connection string
	DB, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_sync=NORMAL")
	if err != nil {
		log.Fatalf("[DB] Failed to open database: %v", err)
	}

	// SQLite handles concurrency better with a single connection in WAL mode
	DB.SetMaxOpenConns(1)

	createTables()
}

func createTables() {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS app_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			app_name TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME,
			duration INTEGER DEFAULT 0,
			status TEXT DEFAULT 'pending'
		);`,
		`CREATE TABLE IF NOT EXISTS sync_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			payload TEXT NOT NULL,
			status TEXT DEFAULT 'pending',
			retries INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS commands (
			id TEXT PRIMARY KEY,
			command TEXT NOT NULL,
			status TEXT DEFAULT 'pending',
			received_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, q := range queries {
		if _, err := DB.Exec(q); err != nil {
			log.Fatalf("[DB] Failed to create table: %v", err)
		}
	}
}
