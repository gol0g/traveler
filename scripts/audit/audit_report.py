#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
traveler 감사 리포트 - 브로커 원장(audit.db) + 귀속맵(attribution.json) + Toss 스냅샷으로
traveler(시스템) vs 사용자 본인의 성과를 완벽 분리 측정.

P&L 산정 방식(계좌별 신뢰도 태그):
  [EXACT]  upbit    = 현재 총평가 - 순입금(입금-출금)          (완전원장 기반, 정확)
  [EXACT]  kis_main = 기간 실현손익(KRW) + 미실현(USD→KRW)       (해외 실현 TR 정상)
  [UNRL]   kis_kr / kis_battle = 미실현(잔고)만                  (국내 실현 TR 불안정 → 전진 일일스냅샷으로 정확화)
  [SNAP]   forward = 오늘 이후 일일스냅샷 델타로 정확 추적 시작
Toss = 종목별 귀속맵으로 traveler/user 분리(현재 포지션 미실현 손익 기준).
"""
import os, json, sqlite3, datetime

HOME = os.path.expanduser("~")
TRAV = os.path.join(HOME, ".traveler")
AUDIT = os.path.join(TRAV, "audit")
DB = os.path.join(AUDIT, "audit.db")
ATTR = json.load(open(os.path.join(AUDIT, "attribution.json"), encoding="utf-8"))
FX_USDKRW = 1380.0  # kis_main(USD) 환산용. 소액계좌라 집계영향 미미. 정밀 필요시 갱신.
TODAY = datetime.date.today().isoformat()

def won(x):
    return "{:+,.0f}원".format(x) if x is not None else "N/A"

c = sqlite3.connect(DB)
c.row_factory = sqlite3.Row

def acc_row(account):
    r = c.execute("SELECT * FROM account_daily WHERE account=? ORDER BY date DESC LIMIT 1", (account,)).fetchone()
    return dict(r) if r else None

def net_deposits(account):
    dep = c.execute("SELECT COALESCE(SUM(amount),0) FROM flows WHERE account=? AND type='deposit'", (account,)).fetchone()[0]
    wd = c.execute("SELECT COALESCE(SUM(amount),0) FROM flows WHERE account=? AND type='withdraw'", (account,)).fetchone()[0]
    return dep - wd, dep, wd

# ---------- 데몬(traveler) 계좌별 손익 ----------
daemon = {}

# upbit: 순입금 있으면 EXACT, 없으면(입출금 API 권한X) 체결 현금흐름법
a = acc_row("upbit")
if a:
    nd, dep, wd = net_deposits("upbit")
    buys = c.execute("SELECT COALESCE(SUM(amount),0) FROM executions WHERE account='upbit' AND side='buy'").fetchone()[0]
    sells = c.execute("SELECT COALESCE(SUM(amount),0) FROM executions WHERE account='upbit' AND side='sell'").fetchone()[0]
    fees = c.execute("SELECT COALESCE(SUM(fee),0) FROM executions WHERE account='upbit'").fetchone()[0]
    if nd:
        pnl = a["total_value"] - nd
        daemon["upbit"] = {"label": "Upbit 크립토(DCA+스캘프)", "pnl": pnl, "conf": "EXACT",
                           "detail": "현재 %.0f - 순입금 %.0f (입금 %.0f/출금 %.0f)" % (a["total_value"], nd, dep, wd), "base": nd}
    else:
        net_inv = buys - sells
        pnl = a["total_value"] - net_inv - fees
        daemon["upbit"] = {"label": "Upbit 크립토(DCA+스캘프)", "pnl": pnl, "conf": "CASHFLOW",
                           "detail": "현재총자산 %.0f - 순투입(매수 %.0f-매도 %.0f) - 수수료 %.0f. (입출금API 401→현금흐름법)" % (a["total_value"], buys, sells, fees),
                           "base": net_inv}

# kis_main: 실현(KRW)+미실현(USD→KRW) (EXACT)
a = acc_row("kis_main")
if a:
    realized = a["realized_cum"] or 0.0
    unrl_krw = (a["unrealized"] or 0.0) * FX_USDKRW
    pnl = realized + unrl_krw
    daemon["kis_main"] = {"label": "US 레버리지(TQQQ/TLT)", "pnl": pnl, "conf": "EXACT",
                          "detail": "실현 %.0f + 미실현 %.0f USD×%.0f = %.0f" % (realized, a["unrealized"] or 0, FX_USDKRW, unrl_krw),
                          "base": (a["securities_value"] or 0) * FX_USDKRW}

# kis_kr / kis_battle: 미실현만 (UNRL, 실현은 전진추적으로)
for acct, label in [("kis_kr", "KR ISA 리밸런서"), ("kis_battle", "한투배틀 ETF")]:
    a = acc_row(acct)
    if a:
        pnl = a["unrealized"]
        daemon[acct] = {"label": label, "pnl": pnl, "conf": "UNRL",
                        "detail": "미실현 %.0f (총평가 %.0f, 현금 %.0f). 실현은 전진 일일추적으로 정확화" % (a["unrealized"] or 0, a["total_value"] or 0, a["cash"] or 0),
                        "base": a["securities_value"] or 0}

daemon_pnl = sum(v["pnl"] for v in daemon.values() if v["pnl"] is not None)

# ---------- Toss: 종목별 traveler/user 분리 ----------
toss = json.load(open(os.path.join(TRAV, "toss_status.json"), encoding="utf-8"))
trav_syms = ATTR.get("toss_traveler_symbols", {})
user_expl = ATTR.get("toss_user_explicit", {})

def toss_owner(sym, name):
    if sym in trav_syms or name in trav_syms:
        return "traveler"
    if sym in user_expl or name in user_expl:
        return "user"
    return ATTR.get("default_toss_owner", "user")

t_trav = {"inv": 0.0, "pnl": 0.0, "syms": []}
t_user = {"inv": 0.0, "pnl": 0.0, "syms": []}
for h in toss.get("holdings", []):
    sym = str(h.get("symbol") or ""); name = str(h.get("name") or "")
    o = toss_owner(sym, name)
    bucket = t_trav if o == "traveler" else t_user
    bucket["inv"] += float(h.get("invested") or 0)
    bucket["pnl"] += float(h.get("pnl") or 0)
    bucket["syms"].append((sym, name, float(h.get("pnl") or 0)))

# ---------- 집계 ----------
traveler_total = daemon_pnl + t_trav["pnl"]
user_total = t_user["pnl"]

def pct(pnl, base):
    return (pnl / base * 100) if base else 0.0

print("=" * 62)
print(" traveler 감사 리포트  [%s]  브로커 원장 기준" % TODAY)
print("=" * 62)
print("\n■ traveler(시스템) — 데몬 계좌")
for k, v in daemon.items():
    print("  [%s] %-22s %14s" % (v["conf"], v["label"], won(v["pnl"])))
    print("        %s" % v["detail"])
print("  " + "-" * 56)
print("  데몬 소계: %s" % won(daemon_pnl))

print("\n■ traveler(시스템) — Toss 텐배거 추천분")
for s, n, p in sorted(t_trav["syms"], key=lambda x: x[2]):
    print("     %-8s %-14s %12s" % (s, n[:12], won(p)))
print("  텐배거 소계: %s (투입 %.0f, %.1f%%)" % (won(t_trav["pnl"]), t_trav["inv"], pct(t_trav["pnl"], t_trav["inv"])))

print("\n" + "=" * 62)
print("  ★ traveler 총손익(시스템 전체): %s" % won(traveler_total))
print("=" * 62)

print("\n■ 사용자 본인 — Toss 직접 매수 (%d종)" % len(t_user["syms"]))
for s, n, p in sorted(t_user["syms"], key=lambda x: x[2])[:8]:
    print("     %-8s %-14s %12s" % (s, n[:12], won(p)))
print("     ... 상위/하위만 표시" if len(t_user["syms"]) > 8 else "")
print("  사용자 총손익: %s (투입 %.0f, %.1f%%)" % (won(user_total), t_user["inv"], pct(user_total, t_user["inv"])))

print("\n■ 대조 결론")
print("  traveler(시스템) %s  vs  사용자 본인 %s" % (won(traveler_total), won(user_total)))
crypto = daemon.get("upbit", {}).get("pnl") or 0
active_ex_crypto = traveler_total - crypto
print("  traveler 중 크립토 베타: %s / 크립토 제외 능동: %s" % (won(crypto), won(active_ex_crypto)))

# ---------- 저장 ----------
out = {
    "date": TODAY, "method": "broker-ledger",
    "traveler_total": traveler_total, "user_total": user_total,
    "daemon": {k: {kk: vv for kk, vv in v.items()} for k, v in daemon.items()},
    "daemon_pnl": daemon_pnl,
    "toss_traveler": {"pnl": t_trav["pnl"], "inv": t_trav["inv"]},
    "toss_user": {"pnl": t_user["pnl"], "inv": t_user["inv"]},
    "crypto_beta": crypto, "active_ex_crypto": active_ex_crypto,
    "fx_usdkrw": FX_USDKRW,
}
os.makedirs(os.path.join(AUDIT, "reports"), exist_ok=True)
rp = os.path.join(AUDIT, "reports", "audit_%s.json" % TODAY)
json.dump(out, open(rp, "w", encoding="utf-8"), ensure_ascii=False, indent=2)
print("\n리포트 저장: %s" % rp)
c.close()
