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

// KRDCADaemon runs weekly KR stock DCA on a schedule
type KRDCADaemon struct {
	engine   *dca.KRDCAEngine
	config   dca.KRDCAConfig
	broker   broker.Broker
	provider provider.Provider
	dataDir  string

	ctx    context.Context
	cancel context.CancelFunc
}

// NewKRDCADaemon creates a new KR DCA daemon
func NewKRDCADaemon(cfg dca.KRDCAConfig, b broker.Broker, p provider.Provider, dataDir string) *KRDCADaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &KRDCADaemon{
		engine:   dca.NewKRDCAEngine(cfg, b, p, dataDir),
		config:   cfg,
		broker:   b,
		provider: p,
		dataDir:  dataDir,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Run starts the KR DCA daemon main loop
func (d *KRDCADaemon) Run() error {
	log.Println("[KR-DCA] Starting KR DCA daemon...")
	log.Printf("[KR-DCA] Target: %s (%s), Base shares: %d, Interval: weekly (%s)",
		d.config.SymbolName, d.config.Symbol, d.config.BaseShares, d.config.DCAWeekday)
	log.Printf("[KR-DCA] RSI enabled: %v, EMA50 enabled: %v, TakeProfit: %v",
		d.config.RSIEnabled, d.config.EMA50Enabled, d.config.TakeProfitEnabled)

	// Save initial status for web UI
	d.saveStatusJSON()

	for {
		nextDCA := d.engine.GetNextDCATime()
		now := time.Now()

		if nextDCA.After(now) {
			waitDuration := time.Until(nextDCA)
			log.Printf("[KR-DCA] Next DCA at %s (in %s)", nextDCA.Format("2006-01-02 15:04 KST"), FormatDuration(waitDuration))

			timer := time.NewTimer(waitDuration)
			select {
			case <-d.ctx.Done():
				timer.Stop()
				log.Println("[KR-DCA] Daemon stopped by signal")
				return nil
			case <-timer.C:
			}
		}

		// Execute DCA cycle
		log.Println("[KR-DCA] ========== KR DCA Cycle Start ==========")
		result, err := d.engine.Run(d.ctx)
		if err != nil {
			log.Printf("[KR-DCA] Cycle failed: %v", err)
		} else {
			log.Printf("[KR-DCA] Cycle #%d: RSI=%.1f (%s), action=%s, shares=%d, amount=₩%.0f, EMA50bonus=%v",
				result.CycleNumber, result.RSI, result.RSILabel, result.Action,
				result.Shares, result.Amount, result.EMA50Bonus)
		}
		log.Println("[KR-DCA] ========== KR DCA Cycle End ==========")

		d.saveStatusJSON()
	}
}

// Stop gracefully stops the daemon
func (d *KRDCADaemon) Stop() {
	d.cancel()
}

// GetEngine returns the KR DCA engine
func (d *KRDCADaemon) GetEngine() *dca.KRDCAEngine {
	return d.engine
}

// saveStatusJSON writes KR DCA status to disk for web UI
func (d *KRDCADaemon) saveStatusJSON() {
	status := d.engine.GetStatus(d.ctx)
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[KR-DCA] Failed to marshal status: %v", err)
		return
	}
	fp := filepath.Join(d.dataDir, "kr_dca_status.json")
	if err := os.WriteFile(fp, data, 0644); err != nil {
		log.Printf("[KR-DCA] Failed to save status: %v", err)
	}
}
