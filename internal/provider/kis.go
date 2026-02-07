package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"traveler/internal/broker/kis"
	"traveler/pkg/model"
)

// KISProvider KIS API 기반 국내주식 데이터 Provider
type KISProvider struct {
	client *kis.Client
}

// NewKISProvider KIS 국내 데이터 Provider 생성
func NewKISProvider(creds kis.Credentials) *KISProvider {
	return &KISProvider{
		client: kis.NewDomesticClient(creds),
	}
}

func (p *KISProvider) Name() string {
	return "kis-domestic"
}

func (p *KISProvider) IsAvailable() bool {
	return p.client.IsReady()
}

func (p *KISProvider) RateLimit() int {
	return 300 // KIS 분당 300
}

// GetDailyCandles 국내주식 일봉 조회
func (p *KISProvider) GetDailyCandles(ctx context.Context, symbol string, days int) ([]model.Candle, error) {
	items, err := p.client.GetDailyCandles(ctx, symbol, days)
	if err != nil {
		return nil, err
	}

	candles := make([]model.Candle, 0, len(items))
	for _, item := range items {
		if item.STCK_BSOP_DATE == "" {
			continue
		}

		t, err := time.Parse("20060102", item.STCK_BSOP_DATE)
		if err != nil {
			continue
		}

		open := parseFloat(item.STCK_OPRC)
		high := parseFloat(item.STCK_HGPR)
		low := parseFloat(item.STCK_LWPR)
		close_ := parseFloat(item.STCK_CLPR)
		volume := parseFloat(item.ACML_VOL)

		if close_ <= 0 {
			continue
		}

		candles = append(candles, model.Candle{
			Time:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close_,
			Volume: int64(volume),
		})
	}

	// 날짜 오름차순 정렬 (KIS는 최신 → 과거 순)
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Time.Before(candles[j].Time)
	})

	// 요청한 일수만큼만 반환
	if len(candles) > days {
		candles = candles[len(candles)-days:]
	}

	return candles, nil
}

// GetIntradayData 미구현 (국내 전략은 일봉 기반)
func (p *KISProvider) GetIntradayData(ctx context.Context, symbol string, date time.Time, interval int) (*model.IntradayData, error) {
	return nil, fmt.Errorf("intraday data not supported for KIS domestic provider")
}

// GetMultiDayIntraday 미구현
func (p *KISProvider) GetMultiDayIntraday(ctx context.Context, symbol string, days int, interval int) ([]model.IntradayData, error) {
	return nil, fmt.Errorf("multi-day intraday not supported for KIS domestic provider")
}

// GetSymbols 미구현 (하드코딩된 유니버스 사용)
func (p *KISProvider) GetSymbols(ctx context.Context, exchange string) ([]model.Stock, error) {
	return nil, fmt.Errorf("symbol listing not supported for KIS domestic provider")
}

// parseFloat 로컬 헬퍼 (kis 패키지의 것은 unexported)
func parseFloat(s string) float64 {
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}
