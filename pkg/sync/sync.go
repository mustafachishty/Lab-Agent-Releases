package sync

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"labguardian/agent/pkg/config"
	"labguardian/agent/pkg/db"
)

type Record struct {
	ID        int    `json:"id"`
	AppName   string `json:"app_name"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Duration  int    `json:"duration"`
}

func StartSyncWorker(cfg *config.Config) {
	for {
		syncPending(cfg)
		time.Sleep(30 * time.Second)
	}
}

func syncPending(cfg *config.Config) {
	// 1. First, move finished sessions into the sync queue
	moveSessionsToQueue()

	// 2. Try to sync the queue
	rows, err := db.DB.Query(`
		SELECT id, payload, retries
		FROM sync_queue WHERE status = 'pending' LIMIT 50
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, retries int
		var payloadStr string
		if err := rows.Scan(&id, &payloadStr, &retries); err != nil {
			continue
		}

		if sendWithRetry(cfg, id, payloadStr, retries) {
			_, _ = db.DB.Exec("DELETE FROM sync_queue WHERE id = ?", id)
		} else {
			_, _ = db.DB.Exec("UPDATE sync_queue SET retries = retries + 1 WHERE id = ?", id)
		}
	}
}

func moveSessionsToQueue() {
	rows, err := db.DB.Query("SELECT id, app_name, start_time, end_time, duration FROM app_usage WHERE status = 'pending'")
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, duration int
		var app, start, end string
		rows.Scan(&id, &app, &start, &end, &duration)

		payload, _ := json.Marshal(map[string]interface{}{
			"type": "app_usage",
			"data": Record{AppName: app, StartTime: start, EndTime: end, Duration: duration},
		})

		_, err = db.DB.Exec("INSERT INTO sync_queue (payload, status) VALUES (?, 'pending')", string(payload))
		if err == nil {
			db.DB.Exec("DELETE FROM app_usage WHERE id = ?", id)
		}
	}
}

func sendWithRetry(cfg *config.Config, id int, payload string, retries int) bool {
	// Exponential backoff: don't retry too fast if we've failed many times
	if retries > 0 {
		wait := time.Duration(retries*retries) * time.Minute
		// If we haven't waited long enough, skip this record for now
		// (Simplified: in a real system we'd check the 'updated_at' timestamp)
		if retries > 5 { return false } 
	}

	req, err := http.NewRequest("POST", cfg.ServerURL+"/api/sync-offline-data", bytes.NewBuffer([]byte(payload)))
	if err != nil { return false }

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil { return false }
	defer resp.Body.Close()

	return resp.StatusCode == 200
}
