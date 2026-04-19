package main

import (
	"log"
	"time"
)

// Reaper periodically scans for expired snaps and purges them.
// In production this would be a separate service or cron job.
type Reaper struct {
	service  *SnapService
	interval time.Duration
	stop     chan struct{}
}

func NewReaper(service *SnapService, interval time.Duration) *Reaper {
	return &Reaper{
		service:  service,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the reaper loop in a background goroutine.
func (r *Reaper) Start() {
	go func() {
		log.Printf("[Reaper] started (interval=%s)", r.interval)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				expired := r.service.ExpireByTTL()
				if expired > 0 {
					log.Printf("[Reaper] purged %d expired snap(s)", expired)
				}
			case <-r.stop:
				log.Println("[Reaper] stopped")
				return
			}
		}
	}()
}

// Stop shuts down the reaper.
func (r *Reaper) Stop() {
	close(r.stop)
}
