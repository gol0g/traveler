# BUG: 1주 포지션 T1 익절 미작동

## 요약
수량이 1주인 포지션은 T1(1차 익절) 조건을 절대 충족할 수 없어서, 수익 구간을 완전히 놓치고 손절만 실행됨.

## 발생 사례: 위메이드 (112040)

### 매매 기록
- **매수**: 2/9 09:18, ₩28,850 x 1주 (pullback 전략)
- **T1**: ₩29,800 (+3.3%)
- **T2**: ₩30,433 (+5.5%)
- **Stop Loss**: ₩28,217 (-2.2%)

### 가격 추이
```
2/9  09:18  ₩28,850  매수
2/10 09:14  ₩29,800  T1 도달 (+₩950) ← 매도 안 됨
2/10 09:55  ₩30,000  T1 초과 (+₩1,150) ← 매도 안 됨
2/11 09:00  ₩29,500  하락 시작
2/12 09:39  ₩28,350  급락 (MA20 이하)
2/12 09:41  ₩28,200  Stop Loss 도달 → 손절 (-₩650, -2.25%)
```

### 핵심
₩1,150 수익(+4.0%)까지 갔던 포지션이 ₩650 손실(-2.25%)로 끝남.
T1 익절이 작동했다면 최소 +₩950(+3.3%) 수익 확보 가능했음.

## 원인

### 코드 위치
`internal/trader/monitor.go` 150번째 줄:
```go
if !active.Target1Hit && currentPrice >= active.Target1 && active.Quantity > 1 {
    halfQty := active.Quantity / 2
```

### 문제
- `Quantity > 1` 조건: "절반 청산" 로직이므로 최소 2주 필요
- `halfQty = 1 / 2 = 0` (정수 나눗셈): 0주 매도는 의미 없음
- **결과**: 1주 포지션은 T1을 영원히 트리거할 수 없음

### 연쇄 효과
1. T1 미작동 → `Target1Hit = false` 유지
2. T2 조건 `Target1Hit && ...` → T2도 미작동
3. Stop Loss만 작동 가능 → 수익 구간 완전 무시

## 영향 범위

### 한국 시장 (KR)
- 주가가 높은 종목 (삼성전자 ₩55,000, 위메이드 ₩28,850 등)
- 포지션 사이징이 소액이면 대부분 1주만 매수
- **거의 모든 KR 대형주가 이 버그에 해당**

### 미국 시장 (US)
- 소액 계좌 ($200)에서 $20+ 주식 매수 시 1주
- 현재 EYE(1주), WBD(1주)도 동일 문제

## 수정 방안

### 방안 1: 1주일 때 T1에서 전량 매도 (권장)
```go
// Target1 도달 - 청산
if !active.Target1Hit && currentPrice >= active.Target1 {
    sellQty := active.Quantity / 2
    if sellQty == 0 {
        sellQty = active.Quantity // 1주면 전량 매도
    }
    // ... 나머지 로직 동일
}
```
- 장점: 수익 확실히 확보, 구현 간단
- 단점: 추가 상승 포기

### 방안 2: 1주일 때 T1에서 Stop을 본전으로 이동만
```go
if !active.Target1Hit && currentPrice >= active.Target1 {
    if active.Quantity > 1 {
        // 기존 절반 매도 로직
    } else {
        // 1주: 매도 안 하고 stop만 본전으로 이동
        active.Target1Hit = true
        active.StopLoss = active.EntryPrice
        log.Printf("[MONITOR] %s: 1-share position, moved stop to breakeven", symbol)
    }
}
```
- 장점: 추가 상승 가능성 유지 + 손실 방어
- 단점: 수익 확보는 안 됨 (다시 하락하면 본전)

### 방안 3: 병행 (T1→Stop 본전, T2→전량 매도)
```go
if !active.Target1Hit && currentPrice >= active.Target1 {
    if active.Quantity > 1 {
        halfQty := active.Quantity / 2
        // 절반 매도
    } else {
        // 1주: stop을 본전으로, T2에서 전량 매도
        active.Target1Hit = true
        active.StopLoss = active.EntryPrice
    }
}
// T2 로직은 Target1Hit && Quantity >= 1 이면 전량 매도
```
- 장점: 1주도 2주 이상과 동일한 전략 구조 유지
- 단점: T1~T2 사이에서 다시 하락 시 본전

## 수정 대상 파일
1. `internal/trader/monitor.go` — T1/T2 조건문 수정 (핵심)
2. `internal/trader/monitor.go` — T2 조건도 확인 (현재 `Target1Hit` 의존)
3. `internal/daemon/daemon.go` — 포지션 복원 시 Target1Hit 상태 반영 확인

## 테스트 방법
1. KR 데몬을 1주짜리 포지션으로 실행
2. 모니터 로그에서 `[TARGET1]` 메시지 확인
3. T1 도달 시 매도 or stop 이동 확인
4. trade_history.json에 "target1" reason 기록 확인

## 관련 이슈
- 위메이드 사례 외에도 293490(카카오게임즈)도 2주 매수 → T1 작동 가능했으나 T1 미도달로 invalidation 손절
- 미국 EYE(1주), WBD(1주)도 현재 동일 위험

## 우선순위
**HIGH** — 소액 계좌에서는 대부분의 포지션이 1~2주이므로, 이 버그는 전체 수익 전략의 핵심 결함.
