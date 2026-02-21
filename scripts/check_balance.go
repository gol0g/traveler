package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"traveler/internal/broker/upbit"
)

func main() {
	accessKey := os.Getenv("UPBIT_ACCESS_KEY")
	secretKey := os.Getenv("UPBIT_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		fmt.Println("UPBIT_ACCESS_KEY / UPBIT_SECRET_KEY not set")
		os.Exit(1)
	}

	client := upbit.NewClientWithKeys(accessKey, secretKey)
	ctx := context.Background()

	bal, err := client.GetBalance(ctx)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== Upbit 잔고 ===\n")
	fmt.Printf("총 자산:  ₩%.0f\n", bal.TotalEquity)
	fmt.Printf("예수금:   ₩%.0f\n", bal.CashBalance)
	fmt.Println()

	pos, err := client.GetPositions(ctx)
	if err == nil && len(pos) > 0 {
		fmt.Println("=== 보유 코인 ===")
		for _, p := range pos {
			val := p.CurrentPrice * p.Quantity
			fmt.Printf("  %s: 수량=%.8f 평단=₩%.0f 현재=₩%.0f 평가=₩%.0f 손익=₩%.0f (%.2f%%)\n",
				p.Symbol, p.Quantity, p.AvgCost, p.CurrentPrice, val, p.UnrealizedPnL, p.UnrealizedPct)
		}
	} else if err != nil {
		fmt.Printf("포지션 조회 오류: %v\n", err)
	}

	raw, _ := json.MarshalIndent(bal, "", "  ")
	fmt.Printf("\nRaw: %s\n", raw)
}
