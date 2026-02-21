package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"traveler/internal/strategy"
)

// filterResult is the JSON response from Gemini for signal filtering
type filterResult struct {
	Symbol string `json:"symbol"`
	Action string `json:"action"` // "PASS" or "REJECT"
	Reason string `json:"reason"`
}

// AIRejection holds info about a rejected signal for display in web UI
type AIRejection struct {
	Symbol string `json:"symbol"`
	Reason string `json:"reason"`
}

// optimizeResult is the JSON response from Gemini for SL/TP optimization
type optimizeResult struct {
	Symbol    string  `json:"symbol"`
	StopLoss  float64 `json:"stop_loss"`
	Target1   float64 `json:"target1"`
	Target2   float64 `json:"target2"`
	Reasoning string  `json:"reasoning"`
}

// FilterSignals sends signals to Gemini for quality evaluation.
// Returns passed signals (with AIReason set) and a list of rejections for web display.
// On any error, returns the original signals unchanged (graceful degradation).
func (c *GeminiClient) FilterSignals(ctx context.Context, signals []strategy.Signal, regime string, market string) ([]strategy.Signal, []AIRejection) {
	if c == nil || len(signals) == 0 {
		return signals, nil
	}

	// Use independent context to avoid inheriting cancelled scan context
	aiCtx, aiCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer aiCancel()

	prompt := buildFilterPrompt(signals, regime, market)
	resp, err := c.Generate(aiCtx, prompt, 500)
	if err != nil {
		log.Printf("[AI] Filter request failed: %v", err)
		return signals, nil
	}

	results, err := parseFilterResults(resp)
	if err != nil {
		log.Printf("[AI] Filter parse failed: %v (raw: %s)", err, truncate(resp, 200))
		return signals, nil
	}

	// Build pass/reject maps
	passReasons := make(map[string]string)
	rejected := make(map[string]string)
	for _, r := range results {
		if strings.EqualFold(r.Action, "REJECT") {
			rejected[r.Symbol] = r.Reason
		} else {
			passReasons[r.Symbol] = r.Reason
		}
	}

	if len(rejected) == 0 {
		log.Printf("[AI] All %d signals passed AI filter", len(signals))
		// Set pass reasons
		for i := range signals {
			if reason, ok := passReasons[signals[i].Stock.Symbol]; ok {
				signals[i].AIReason = reason
			}
		}
		return signals, nil
	}

	// Filter out rejected signals, annotate passed ones
	var passed []strategy.Signal
	var rejections []AIRejection
	for _, sig := range signals {
		if reason, rej := rejected[sig.Stock.Symbol]; rej {
			log.Printf("[AI] REJECT %s: %s", sig.Stock.Symbol, reason)
			rejections = append(rejections, AIRejection{Symbol: sig.Stock.Symbol, Reason: reason})
		} else {
			if reason, ok := passReasons[sig.Stock.Symbol]; ok {
				sig.AIReason = reason
			}
			passed = append(passed, sig)
		}
	}

	log.Printf("[AI] Filter: %d → %d signals (%d rejected)", len(signals), len(passed), len(rejected))
	return passed, rejections
}

// OptimizeGuides sends each signal to Gemini individually for optimized SL/TP levels.
// One request per signal to avoid response truncation.
// Updates Guide fields in-place. On error per signal, that signal keeps original levels.
func (c *GeminiClient) OptimizeGuides(ctx context.Context, signals []strategy.Signal, regime string, market string) []strategy.Signal {
	if c == nil || len(signals) == 0 {
		return signals
	}

	optimized := 0
	for i := range signals {
		if signals[i].Guide == nil {
			continue
		}

		aiCtx, aiCancel := context.WithTimeout(context.Background(), 30*time.Second)
		prompt := buildOptimizePromptSingle(signals[i], regime, market)
		resp, err := c.Generate(aiCtx, prompt, 800)
		aiCancel()
		if err != nil {
			log.Printf("[AI] Optimize %s failed: %v", signals[i].Stock.Symbol, err)
			continue
		}

		results, err := parseOptimizeResults(resp)
		if err != nil {
			log.Printf("[AI] Optimize %s parse failed: %v (raw: %s)", signals[i].Stock.Symbol, err, truncate(resp, 200))
			continue
		}
		if len(results) == 0 {
			continue
		}

		opt := results[0]

		g := signals[i].Guide

		// Calculate proposed levels (fallback to current if AI didn't suggest)
		newSL := g.StopLoss
		newT1 := g.Target1
		newT2 := g.Target2

		if opt.StopLoss > 0 && opt.StopLoss < g.EntryPrice {
			newSL = opt.StopLoss
		}
		if opt.Target1 > 0 && opt.Target1 > g.EntryPrice {
			newT1 = opt.Target1
		}
		if opt.Target2 > 0 && opt.Target2 > g.EntryPrice {
			newT2 = opt.Target2
		}

		// Validate as a whole: R/R must be >= 1.5 with new levels
		newRisk := g.EntryPrice - newSL
		newReward := newT1 - g.EntryPrice
		if newRisk <= 0 || newReward <= 0 {
			log.Printf("[AI] SKIP %s: invalid levels (SL=%.2f T1=%.2f entry=%.2f)",
				signals[i].Stock.Symbol, newSL, newT1, g.EntryPrice)
			continue
		}
		newRR := newReward / newRisk
		if newRR < 1.5 {
			log.Printf("[AI] SKIP %s: R/R %.2f < 1.5 (SL=%.2f T1=%.2f)",
				signals[i].Stock.Symbol, newRR, newSL, newT1)
			continue
		}

		// Check if anything actually changed
		if newSL == g.StopLoss && newT1 == g.Target1 && newT2 == g.Target2 {
			continue
		}

		// Apply validated levels
		g.StopLoss = newSL
		g.StopLossPct = (g.EntryPrice - newSL) / g.EntryPrice * 100
		g.Target1 = newT1
		g.Target1Pct = (newT1 - g.EntryPrice) / g.EntryPrice * 100
		if newT2 > newT1 {
			g.Target2 = newT2
			g.Target2Pct = (newT2 - g.EntryPrice) / g.EntryPrice * 100
		}
		g.RiskRewardRatio = newRR
		signals[i].AIOptimizeReason = opt.Reasoning
		optimized++
		log.Printf("[AI] OPTIMIZE %s: SL=%.2f T1=%.2f T2=%.2f R/R=%.2f (%s)",
			signals[i].Stock.Symbol, g.StopLoss, g.Target1, g.Target2, g.RiskRewardRatio, opt.Reasoning)
	}

	if optimized > 0 {
		log.Printf("[AI] Optimized SL/TP for %d/%d signals", optimized, len(signals))
	}
	return signals
}

func buildFilterPrompt(signals []strategy.Signal, regime, market string) string {
	var sb strings.Builder
	sb.WriteString("You are a portfolio risk manager reviewing algorithmic trading signals.\n")
	sb.WriteString("Technical analysis is ALREADY DONE by the algorithm. Do NOT re-evaluate technicals.\n")
	sb.WriteString("Your job: assess RISKS the algorithm CANNOT detect.\n\n")
	sb.WriteString(fmt.Sprintf("Market: %s, Regime: %s\n", market, regime))
	sb.WriteString(fmt.Sprintf("Date: %s\n\n", time.Now().Format("2006-01-02")))
	sb.WriteString("SIGNALS:\n")

	for i, sig := range signals {
		g := sig.Guide
		entry, sl, t1, rr := 0.0, 0.0, 0.0, 0.0
		if g != nil {
			entry = g.EntryPrice
			sl = g.StopLoss
			t1 = g.Target1
			rr = g.RiskRewardRatio
		}

		sb.WriteString(fmt.Sprintf("%d. %s: %s | Entry %.2f | SL %.2f | T1 %.2f | R/R %.2f | Prob %.0f%%\n",
			i+1, sig.Stock.Symbol, sig.Strategy, entry, sl, t1, rr, sig.Probability))
		sb.WriteString(fmt.Sprintf("   Reason: %s\n", sig.Reason))
	}

	sb.WriteString("\nEvaluate each signal for RISK FACTORS only:\n")
	sb.WriteString("1. EARNINGS/EVENT RISK: Is earnings report imminent? FDA decision, product launch, legal ruling?\n")
	sb.WriteString("   Swing trades (5-20 day hold) through earnings are dangerous.\n")
	sb.WriteString("2. SECTOR HEADWINDS: Is this sector facing regulatory pressure, rotation out, or macro headwinds?\n")
	sb.WriteString("3. CONCENTRATION: Multiple signals in same sector = correlated risk. Flag if >2 in one sector.\n")
	sb.WriteString("4. MACRO MISMATCH: Does this trade conflict with current macro environment?\n")
	sb.WriteString("   (e.g., leveraged bull ETF in deteriorating macro, defensive stock in risk-on regime)\n")
	sb.WriteString("5. COMPANY-SPECIFIC: Known fundamental problems? Declining business? Accounting concerns?\n")
	sb.WriteString("\nREJECT only for CLEAR risk factors. If no risks found, PASS.\n")
	sb.WriteString("Do NOT reject for technical reasons — the algorithm already handles that.\n\n")
	sb.WriteString("Respond with JSON array:\n")
	sb.WriteString(`[{"symbol":"AAPL","action":"PASS","reason":"no imminent risks, solid business"},`)
	sb.WriteString("\n")
	sb.WriteString(` {"symbol":"MSFT","action":"REJECT","reason":"earnings in 3 days, high IV risk for swing trade"}]`)
	sb.WriteString("\nRespond ONLY with JSON array.")

	return sb.String()
}

func buildOptimizePromptSingle(sig strategy.Signal, regime, market string) string {
	g := sig.Guide
	var sb strings.Builder
	sb.WriteString("You are a risk management specialist. Optimize stop-loss and target levels for this trade.\n")
	sb.WriteString(fmt.Sprintf("Market: %s, Regime: %s\n\n", market, regime))

	sb.WriteString(fmt.Sprintf("%s: Entry %.2f | SL %.2f | T1 %.2f | T2 %.2f | ATR(14) %.2f\n",
		sig.Stock.Symbol, g.EntryPrice, g.StopLoss, g.Target1, g.Target2, g.EntryATR))
	sb.WriteString(fmt.Sprintf("Strategy: %s | Reason: %s\n\n", sig.Strategy, sig.Reason))

	// Include last 20 candles
	nCandles := 20
	if len(sig.Candles) < nCandles {
		nCandles = len(sig.Candles)
	}
	if nCandles >= 5 {
		sb.WriteString("Recent candles (O/H/L/C/Vol):\n")
		start := len(sig.Candles) - nCandles
		for j := start; j < len(sig.Candles); j++ {
			c := sig.Candles[j]
			sb.WriteString(fmt.Sprintf("D%d: %.2f/%.2f/%.2f/%.2f vol=%d\n",
				j-start+1, c.Open, c.High, c.Low, c.Close, c.Volume))
		}
	}

	sb.WriteString("\nAnalyze the price action and identify KEY LEVELS:\n")
	sb.WriteString("1. Find swing lows = SUPPORT (multiple touches, high-volume bounces)\n")
	sb.WriteString("2. Find swing highs = RESISTANCE (prior rejection points)\n")
	sb.WriteString("3. Place SL just below nearest significant support\n")
	sb.WriteString("4. Set T1 at nearest resistance, T2 at next resistance beyond T1\n")
	sb.WriteString("5. SL must be > 0.5x ATR from entry to avoid noise\n")
	sb.WriteString("6. Maintain R/R >= 1.5\n")
	sb.WriteString("7. If current levels are already good, return them unchanged\n\n")
	sb.WriteString(fmt.Sprintf("Respond with JSON array (1 element for %s):\n", sig.Stock.Symbol))
	sb.WriteString(`[{"symbol":"AAPL","stop_loss":148.50,"target1":158.00,"target2":165.00,"reasoning":"SL below swing low 149, T1 prior high 158"}]`)
	sb.WriteString("\nKeep reasoning under 30 words. Respond ONLY with JSON array, no markdown.")

	return sb.String()
}

func parseFilterResults(resp string) ([]filterResult, error) {
	resp = extractJSON(resp)
	var results []filterResult
	if err := json.Unmarshal([]byte(resp), &results); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return results, nil
}

func parseOptimizeResults(resp string) ([]optimizeResult, error) {
	resp = extractJSON(resp)
	var results []optimizeResult
	if err := json.Unmarshal([]byte(resp), &results); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return results, nil
}

// extractJSON tries to find a JSON array in the response text.
// Gemini sometimes wraps JSON in markdown code blocks.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)

	// Strip markdown code block if present
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var jsonLines []string
		inside := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inside = !inside
				continue
			}
			if inside {
				jsonLines = append(jsonLines, line)
			}
		}
		s = strings.Join(jsonLines, "\n")
	}

	// Find the JSON array
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}

	return s
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
