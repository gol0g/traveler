package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"traveler/internal/provider"
	"traveler/internal/strategy"
	"traveler/internal/symbols"
	"traveler/pkg/model"
)

func main() {
	fmt.Println("============================================================")
	fmt.Println(" CRYPTO META STRATEGY - 모의 테스트")
	fmt.Println("============================================================")
	fmt.Println()

	ctx := context.Background()
	prov := provider.NewUpbitProvider()

	// 1. BTC 데이터로 레짐 감지
	fmt.Println("[1] BTC 시장 레짐 감지 중...")
	meta := strategy.NewCryptoMetaStrategy(prov)
	regime := meta.GetCurrentRegime(ctx)
	regimeLabel := map[strategy.Regime]string{
		strategy.RegimeBull:     "🟢 BULL (강세)",
		strategy.RegimeSideways: "🟡 SIDEWAYS (횡보)",
		strategy.RegimeBear:     "🔴 BEAR (약세)",
	}
	fmt.Printf("    현재 레짐: %s\n", regimeLabel[regime])

	// BTC 지표 출력
	btcCandles, err := prov.GetDailyCandles(ctx, "KRW-BTC", 55)
	if err != nil {
		fmt.Printf("    BTC 캔들 조회 실패: %v\n", err)
		os.Exit(1)
	}
	btcInd := strategy.CalculateIndicators(btcCandles)
	btcPrice := btcCandles[len(btcCandles)-1].Close
	fmt.Printf("    BTC 현재가: ₩%.0f\n", btcPrice)
	fmt.Printf("    MA20: ₩%.0f (slope: %.2f%%)\n", btcInd.MA20, btcInd.MA20Slope)
	fmt.Printf("    MA50: ₩%.0f\n", btcInd.MA50)
	fmt.Printf("    RSI14: %.1f\n", btcInd.RSI14)
	fmt.Printf("    BB: ₩%.0f ~ ₩%.0f (width: %.1f%%)\n", btcInd.BBLower, btcInd.BBUpper, btcInd.BBWidth)
	fmt.Println()

	// 2. 레짐에 따른 전략 설명
	fmt.Println("[2] 레짐별 적용 전략:")
	switch regime {
	case strategy.RegimeBull:
		fmt.Println("    → Volatility Breakout (래리 윌리엄스 돌파매매)")
		fmt.Println("    → Volume Spike (거래량 급증 역전 매수)")
	case strategy.RegimeSideways:
		fmt.Println("    → Range Trading (지지선 매수, 저항선 매도)")
		fmt.Println("    → RSI Contrarian (RSI<25 극단 역추세)")
		fmt.Println("    → Volume Spike (거래량 급증 역전 매수)")
	case strategy.RegimeBear:
		fmt.Println("    → RSI Contrarian 극보수적 (RSI<20 에서만 진입)")
	}
	fmt.Println()

	// 3. 유니버스 스캔
	cryptoSyms := symbols.GetUniverse(symbols.UniverseCryptoTop10)
	if cryptoSyms == nil {
		fmt.Println("크립토 유니버스 없음")
		os.Exit(1)
	}
	fmt.Printf("[3] 크립토 Top10 스캔 중 (%d종목)...\n", len(cryptoSyms))

	var signalCount int
	scanStart := time.Now()

	for _, sym := range cryptoSyms {
		name := symbols.GetCryptoSymbolName(sym)
		stock := model.Stock{Symbol: sym, Name: name}

		sig, err := meta.Analyze(ctx, stock)
		if err != nil {
			fmt.Printf("    ❌ %s (%s): 오류 - %v\n", sym, name, err)
			continue
		}

		if sig == nil {
			fmt.Printf("    ⬜ %s (%s): 시그널 없음\n", sym, name)
		} else {
			signalCount++
			fmt.Printf("    ✅ %s (%s):\n", sym, name)
			fmt.Printf("       전략: %s | 강도: %.0f | 확률: %.0f%%\n",
				sig.Strategy, sig.Strength, sig.Probability)
			fmt.Printf("       사유: %s\n", sig.Reason)
			if sig.Guide != nil {
				g := sig.Guide
				fmt.Printf("       진입: ₩%.0f | 손절: ₩%.0f (%.1f%%) | T1: ₩%.0f (+%.1f%%) | T2: ₩%.0f (+%.1f%%)\n",
					g.EntryPrice, g.StopLoss, g.StopLossPct,
					g.Target1, g.Target1Pct, g.Target2, g.Target2Pct)
			}
		}

		time.Sleep(200 * time.Millisecond) // API 간격
	}

	scanTime := time.Since(scanStart)
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Printf(" 스캔 완료: %d / %d 시그널 (%.1f초)\n", signalCount, len(cryptoSyms), scanTime.Seconds())
	fmt.Printf(" 현재 레짐: %s\n", regimeLabel[regime])
	if regime == strategy.RegimeBear {
		fmt.Println(" 약세장 → 모든 매매 스킵 (현금 보존)")
	} else if signalCount == 0 {
		fmt.Println(" 조건 충족 종목 없음 → 매매 대기")
	} else {
		fmt.Printf(" %d개 매매 기회 발견 (모의 테스트이므로 실행 안함)\n", signalCount)
	}
	fmt.Println("============================================================")
}
