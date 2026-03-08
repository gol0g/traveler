package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"traveler/internal/broker"
	"traveler/internal/dca"
	"traveler/internal/provider"
)

// DCADaemon runs long-term DCA investing on a schedule
type DCADaemon struct {
	engine   *dca.Engine
	config   dca.Config
	broker   broker.Broker
	provider provider.Provider
	dataDir  string

	ctx    context.Context
	cancel context.CancelFunc
}

// NewDCADaemon creates a new DCA daemon
func NewDCADaemon(cfg dca.Config, b broker.Broker, p provider.Provider, dataDir string) *DCADaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &DCADaemon{
		engine:   dca.NewEngine(cfg, b, p, dataDir),
		config:   cfg,
		broker:   b,
		provider: p,
		dataDir:  dataDir,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Run starts the DCA daemon main loop
func (d *DCADaemon) Run() error {
	log.Println("[DCA] Starting DCA daemon...")
	log.Printf("[DCA] Base amount: ₩%.0f, Interval: %s, Targets: %d coins",
		d.config.BaseDCAAmount, d.config.Interval, len(d.config.Targets))
	log.Printf("[DCA] F&G enabled: %v, EMA50 enabled: %v, Rebalance: %v (%.0f%%)",
		d.config.FearGreedEnabled, d.config.EMA50Enabled,
		d.config.RebalanceEnabled, d.config.RebalanceThreshold)

	// Save initial status for web UI
	d.saveStatusJSON()

	for {
		nextDCA := d.engine.GetNextDCATime()
		now := time.Now()

		if nextDCA.After(now) {
			waitDuration := time.Until(nextDCA)
			log.Printf("[DCA] Next DCA at %s (in %s)", nextDCA.Format("2006-01-02 15:04 KST"), FormatDuration(waitDuration))

			// Wait until next DCA time or context cancellation
			timer := time.NewTimer(waitDuration)
			select {
			case <-d.ctx.Done():
				timer.Stop()
				log.Println("[DCA] Daemon stopped by signal")
				return nil
			case <-timer.C:
				// Time to execute DCA
			}
		}

		// Execute DCA cycle
		log.Println("[DCA] ========== DCA Cycle Start ==========")
		result, err := d.engine.Run(d.ctx)
		if err != nil {
			log.Printf("[DCA] Cycle failed: %v", err)
		} else {
			log.Printf("[DCA] Cycle #%d complete: F&G=%d (%s), mult=%.2fx, spent=₩%.0f, buys=%d, sells=%d, rebalanced=%v",
				result.CycleNumber, result.FearGreed, result.FGLabel, result.Multiplier,
				result.TotalSpent, len(result.Buys), len(result.Sells), result.Rebalanced)

			// Check restore signal: F&G recovered from extreme fear
			if d.config.ReducedMode && result.FearGreed >= d.config.RestoreFGThreshold {
				log.Println("[DCA] !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				log.Printf("[DCA] RESTORE SIGNAL: F&G=%d — Extreme Fear 탈출!", result.FearGreed)
				log.Printf("[DCA] 기본금액 ₩%.0f → ₩%.0f 원복을 권장합니다", d.config.BaseDCAAmount, d.config.OriginalAmount)
				log.Println("[DCA] !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			}
		}
		log.Println("[DCA] ========== DCA Cycle End ==========")

		// Save status JSON for web UI
		d.saveStatusJSON()
	}
}

// Stop gracefully stops the DCA daemon
func (d *DCADaemon) Stop() {
	d.cancel()
}

// GetEngine returns the DCA engine (for web API status)
func (d *DCADaemon) GetEngine() *dca.Engine {
	return d.engine
}

// saveStatusJSON writes DCA status to disk for the web UI
func (d *DCADaemon) saveStatusJSON() {
	status := d.engine.GetStatus(d.ctx)
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[DCA] Failed to marshal status: %v", err)
		return
	}
	fp := filepath.Join(d.dataDir, "dca_status.json")
	if err := os.WriteFile(fp, data, 0644); err != nil {
		log.Printf("[DCA] Failed to save status: %v", err)
	}
}
