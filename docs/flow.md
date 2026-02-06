# Traveler Trading Daemon - Flow Diagram

```mermaid
flowchart TB
    subgraph STARTUP["DAEMON STARTUP (Run)"]
        S1[Wake Monitor] --> S2{Market Open?}
        S2 -->|No| S3{WaitForMarket?}
        S3 -->|Yes| S4[Wait TimeToOpen]
        S3 -->|No| STOP1[Shutdown: market_closed]
        S4 --> S2
        S2 -->|Yes| S5[Get Balance]
        S5 --> S6[Start Daily Tracker]
        S6 --> S7[Sizer: AdjustConfigForBalance]
        S7 --> S8["Init PlanStore (~/.traveler/plans.json)"]
        S8 --> S9[Create AutoTrader + PlanStore]
        S9 --> S10[Get Existing Positions from Broker]
        S10 --> S11{For Each Position}
    end

    subgraph RESTORE["POSITION RESTORE"]
        S11 --> R1{PlanStore에 플랜 있음?}
        R1 -->|Yes| R2["Restore: 원래 전략/stop/target 복원"]
        R1 -->|No| R3["generatePlanFromAnalysis()"]
        R3 --> R4["GetDailyCandles(50)"]
        R4 --> R5["CalculateIndicators (MA20,RSI,BB)"]
        R5 --> R6{"inferStrategy()"}
        R6 -->|"RSI<40 + close<MA20"| R7[mean-reversion]
        R6 -->|"close>MA50 + close>MA20*1.02"| R8[breakout]
        R6 -->|그 외| R9[pullback]
        R7 --> R10[R 기반 stop/target 계산]
        R8 --> R10
        R9 --> R10
        R10 --> R11a[PlanStore에 저장]
        R11a --> R2
        R2 --> R12["RegisterPositionWithPlan()"]
        R3 -.->|캔들 조회 실패| R13["Fallback: 고정 2%/4%"]
        R13 --> R12
    end

    S11 --> MAINLOOP

    subgraph MAINLOOP["MAIN LOOP"]
        ML1["runInvalidationCheck() - 1회"] --> ML2["runScanCycle() - 1회"]
        ML2 --> ML3["Monitor-Only Mode 진입"]
        ML3 --> ML4{30초마다}
        ML4 --> ML5["runMonitorCycle()"]
        ML5 --> ML6{"checkStopConditions()"}
        ML6 -->|market closed / daily limit| STOP2[Shutdown]
        ML6 -->|continue| ML4
    end

    subgraph INVALIDATION["INVALIDATION CHECK (장 시작 1회)"]
        IV1[For Each Active Position] --> IV2{Strategy?}
        IV2 -->|pullback| IV3["GetDailyCandles(30)"]
        IV3 --> IV4{"close < MA20?"}
        IV4 -->|Yes| IV5[ConsecutiveDaysBelow++]
        IV5 --> IV6{"2일 연속?"}
        IV6 -->|Yes| IV_SELL["ClosePosition: 무효화"]
        IV6 -->|No| IV_NEXT[다음 종목]
        IV4 -->|No| IV4R[Reset counter=0]
        IV4R --> IV_NEXT

        IV2 -->|breakout| IV7["GetDailyCandles(5)"]
        IV7 --> IV8{"close < BreakoutLevel?"}
        IV8 -->|Yes| IV_SELL
        IV8 -->|No| IV_NEXT

        IV2 -->|mean-reversion| IV9{"보유 2일 이상?"}
        IV9 -->|No| IV_NEXT
        IV9 -->|Yes| IV10["GetDailyCandles(30)"]
        IV10 --> IV11{"RSI<35 AND close<BB하단?"}
        IV11 -->|Yes| IV_SELL
        IV11 -->|No| IV_NEXT
    end

    subgraph SCAN["MULTI-STRATEGY SCAN (장 시작 1회)"]
        SC1["strategy.GetAll() - 3개 전략"]
        SC1 --> SC2["Russell Universe 200 종목 로드"]
        SC2 --> SC3{For Each Stock}
        SC3 --> SC4["pullback.Analyze()"]
        SC3 --> SC5["mean-reversion.Analyze()"]
        SC3 --> SC6["breakout.Analyze()"]
        SC4 & SC5 & SC6 --> SC7["가장 강한 Signal만 유지"]
        SC7 --> SC8["PositionSizer 적용"]
        SC8 --> SC9["ExecuteSignals()"]
    end

    subgraph EXECUTE["EXECUTE SIGNALS"]
        EX1["GetPositions()"] --> EX2["ValidateSignals()"]
        EX2 --> EX3{검증}
        EX3 -->|"이미 보유"| EX_REJ1[REJECT: already holding]
        EX3 -->|"슬롯 초과"| EX_REJ2[REJECT: max positions]
        EX3 -->|"투자금 초과"| EX_REJ3[REJECT: exceeds max size]
        EX3 -->|통과| EX4["broker.PlaceOrder(BUY)"]
        EX4 -->|성공| EX5["RegisterPositionWithPlan()<br/>(strategy, stop, target, maxDays)"]
        EX5 --> EX6["planStore.Save()<br/>(~/.traveler/plans.json)"]
        EX6 --> EX7["tracker.RecordTrade()"]
    end

    subgraph MONITOR["MONITOR CYCLE (30초마다)"]
        direction TB
        MO1["For Each Position: GetQuote()"] --> MO2{"1. price <= StopLoss?"}
        MO2 -->|Yes| MO_SELL1["STOP LOSS - 전량 매도"]
        MO2 -->|No| MO3{"2. Target1Hit AND price >= Target2?"}
        MO3 -->|Yes| MO_SELL2["TARGET2 - 전량 매도"]
        MO3 -->|No| MO4{"3. price >= Target1? (qty>1)"}
        MO4 -->|Yes| MO5["TARGET1 - 절반 매도<br/>Stop을 본전으로 이동<br/>planStore.UpdateTarget1Hit()"]
        MO4 -->|No| MO6{"4. 보유일 >= MaxHoldDays?"}
        MO6 -->|Yes| MO_SELL3["TIME STOP - 전량 매도"]
        MO6 -->|No| MO7[Hold - 다음 체크까지 대기]
    end

    subgraph SELL["POSITION CLOSE"]
        SELL1["executor.ExecuteSell()"] --> SELL2["UnregisterPosition()"]
        SELL2 --> SELL3["planStore.Delete()"]
        SELL3 --> SELL4["슬롯 해제 - 다음 스캔에서 새 매수 가능"]
    end

    MO_SELL1 & MO_SELL2 & MO_SELL3 --> SELL
    IV_SELL --> SELL
```

## 전략별 설정

| | Pullback | Mean Reversion | Breakout |
|---|---|---|---|
| **무효화** | close < MA20 2일 연속 | RSI<35 + BB하단 (2일후) | close < 돌파레벨 |
| **Time Stop** | 7 거래일 | 5 거래일 | 15 거래일 |
| **Target1** | 1.5R | MA20 (평균회귀) | 1.5R |
| **Target2** | 2.5R | BB 상단 | 3.0R |

## 청산 우선순위

1. **손절** (price <= StopLoss) — 리스크 제한
2. **익절** (Target1 → Target2) — 수익 실현
3. **전략 무효화** (장 시작 1회) — 틀린 트레이드 자르기
4. **Time Stop** (N거래일 초과) — 안 움직이는 트레이드 자르기

## 파일 경로

- **PlanStore**: `~/.traveler/plans.json`
- **Daily Reports**: `~/.traveler/daily_YYYY-MM-DD.json`
