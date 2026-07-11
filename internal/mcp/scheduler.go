package mcp

import (
	"context"
	"log"
	"time"
)

// Metadata scheduler (improvement: 스케줄러). When -sync-interval and
// -sync-source are set, the server periodically runs an incremental metadata
// sync against the source and logs a one-line digest (change count + quality
// score + release-gate status). It never mutates business meaning — the sync
// stays incremental and deletions remain retire-candidates — so it is safe to
// leave running. Cron-free; lives in the server process.

// SchedulerConfig configures the background metadata loop.
type SchedulerConfig struct {
	Source     string        // db profile id to sync (required to sync)
	Interval   time.Duration // <=0 disables the whole scheduler
	WebhookURL string        // optional: POST the digest here each tick
}

// StartScheduler launches the background loop unless interval<=0. The loop
// stops when ctx is canceled. The first run fires after one interval (not at
// boot) to avoid racing startup. A webhook with no source still fires the
// digest push on schedule (sync is skipped).
func (s *Server) StartScheduler(ctx context.Context, cfg SchedulerConfig) {
	if cfg.Interval <= 0 || (cfg.Source == "" && cfg.WebhookURL == "") {
		return
	}
	if cfg.Interval < time.Minute {
		cfg.Interval = time.Minute // floor: never hammer the source DB
	}
	log.Printf("metadata scheduler: every %s (source=%q webhook=%v)", cfg.Interval, cfg.Source, cfg.WebhookURL != "")
	go func() {
		t := time.NewTicker(cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("metadata scheduler stopped")
				return
			case <-t.C:
				if cfg.Source != "" {
					s.runScheduledSync(ctx, cfg.Source)
				}
				if cfg.WebhookURL != "" {
					s.postDigestWebhook(ctx, cfg.WebhookURL, s.MetadataDigest())
				}
			}
		}
	}()
}

func (s *Server) runScheduledSync(ctx context.Context, source string) {
	// bound each run so a stuck DB cannot wedge the ticker goroutine
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	res := s.mcpRunMetadataSync(runCtx, source, nil, true, false)
	if status, _ := res["status"].(string); status != "ok" {
		log.Printf("scheduled sync[%s] failed: %v", source, res["error"])
		s.appendAudit(map[string]any{
			"ts": time.Now().Format(time.RFC3339Nano), "tool": "scheduler:sync",
			"detail": source, "is_error": true, "error": res["error"],
		})
		return
	}
	changes := 0
	if n, ok := res["change_count"].(int); ok {
		changes = n
	}
	skipped, _ := res["skipped"].(bool)

	q := s.cat().QualityReport()
	gate := s.cat().QualityGate()
	log.Printf("scheduled sync[%s]: changes=%d skipped=%v quality=%.1f(%s) gate=%v",
		source, changes, skipped, q.OverallScore, q.OverallGrade, gate.Pass)
	s.appendAudit(map[string]any{
		"ts": time.Now().Format(time.RFC3339Nano), "tool": "scheduler:sync",
		"detail":        source,
		"change_count":  changes,
		"skipped":       skipped,
		"quality_score": q.OverallScore,
		"gate_pass":     gate.Pass,
	})
}
