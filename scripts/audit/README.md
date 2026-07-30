# traveler 감사 시스템 (broker-ledger audit)

traveler(시스템) 거래와 사용자 본인 거래를 **완벽 분리·추적·측정**하는 감사 계기판.
브로커(Upbit·KIS)의 완전한 원장을 권위로, **append-only** 로 적재한다.

> 배경: 데몬이 자기 거래이력을 ~90건 rolling window로 버려(과거 영구소실) 자체 로그 수치가 크게 틀렸음
> (Upbit 자체로그 −3% vs 브로커 +23.5%). 브로커 원장만이 유일 권위.

## 배포 위치

Pi: `~/.traveler/audit/` (cron 매일 08:30 pull, 08:36 report). 이 repo가 소스 백업(사고 재발 방지).

## 파일

| 파일 | 역할 |
|---|---|
| `attribution.json` | 거래 귀속 **단일 권위**. 데몬 계좌=traveler 전량, Toss=종목별(텐배거 4종만 traveler). 수정 후 `audit_report.py` 재실행 |
| `broker_pull.py` | 브로커 API → `audit.db`(SQLite) idempotent 적재. 체결·잔고·실현·스냅샷 |
| `audit_report.py` | 원장+귀속맵+`toss_status.json` → traveler vs 사용자 손익 분리 → `reports/audit_YYYY-MM-DD.json` |

## 보안·운영 원칙

1. **시크릿(키·토큰)을 코드/DB/출력에 절대 저장·노출 안 함.** 런타임에 `~/.traveler/.env`만 읽음.
2. **KIS 토큰은 데몬 캐시(`~/.kis_token_*.json`) 재사용** — 신규발급 금지(데몬 무효화·rate-limit 방지). 만료 시 해당 계좌 skip.
3. **Upbit JWT는 요청별 서명**(콜론 보존: `urlencode(..., safe=":")` — 안 하면 query_hash 불일치 401).
4. 적재는 브로커 고유 id로 dedup → 매일 돌려도 원장 안 깨짐.

## 실행

```bash
cd ~/.traveler/audit
python3 broker_pull.py     # 원장 적재
python3 audit_report.py    # 성과분리 리포트
```

## 알려진 한계

- **KIS 국내 실현손익**: 기간손익 TR(TTTC8494R)이 rlzt_pfls=0만 반환 → 배틀·ISA는 미실현만(UNRL 태그). 전진 일일스냅샷(account_daily) 델타로 실현 포함 정확화 예정.
- **Upbit 입출금 API 401**(키 권한 없음) → 순입금법 대신 체결 현금흐름법.
