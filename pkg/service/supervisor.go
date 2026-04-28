package service

import (
	"log"
	"time"
)

// SafeRun wraps a function in a recovery loop. 
// If the function panics, it restarts it after a short delay.
func SafeRun(name string, fn func()) {
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[SUPERVISOR] %s crashed: %v. Restarting in 5s...", name, r)
					time.Sleep(5 * time.Second)
				}
			}()
			
			log.Printf("[SUPERVISOR] Starting %s...", name)
			fn()
		}()
	}
}
