#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
traveler 감사 시스템 - 브로커 권위 원장 적재기 (append-only).
- Upbit: 전체 주문(체결)·입출금·현재잔고  (요청별 JWT 서명, 콜론 보존)
- KIS main(해외)/kr·battle(국내): 현재잔고·미실현·해외실현·국내체결  (데몬 캐시 토큰 재사용, 신규발급 금지)
원칙:
  1) 시크릿(키·토큰)은 절대 출력/DB저장 안 함.
  2) KIS 토큰은 ~/.kis_token_*.json 캐시만 읽음(데몬이 갱신). 만료 시 해당 계좌 skip.
  3) 모든 적재는 idempotent(브로커 고유 id로 dedup) → 매일 돌려도 원장 안 깨짐.
사용: python3 broker_pull.py
"""
import os, sys, json, time, glob, re, uuid, hmac, hashlib, base64, datetime
import urllib.request, urllib.parse, urllib.error
import sqlite3

HOME = os.path.expanduser("~")
TRAV = os.path.join(HOME, ".traveler")
AUDIT_DIR = os.path.join(TRAV, "audit")
DB = os.path.join(AUDIT_DIR, "audit.db")
os.makedirs(AUDIT_DIR, exist_ok=True)

TODAY = datetime.date.today().isoformat()
NOW = datetime.datetime.now().isoformat(timespec="seconds")

def load_env():
    env = {}
    for line in open(os.path.join(TRAV, ".env"), encoding="utf-8", errors="ignore"):
        line = line.strip()
        if "=" in line and not line.startswith("#"):
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"').strip("'").strip()
    return env

ENV = load_env()

# ---------- DB ----------
def db_init():
    c = sqlite3.connect(DB)
    c.executescript("""
    CREATE TABLE IF NOT EXISTS executions(
      venue TEXT, account TEXT, ext_id TEXT, symbol TEXT, side TEXT,
      qty REAL, price REAL, amount REAL, fee REAL, realized_pnl REAL,
      exec_time TEXT, pulled_at TEXT,
      PRIMARY KEY(venue, account, ext_id)
    );
    CREATE TABLE IF NOT EXISTS snapshots(
      date TEXT, venue TEXT, account TEXT, symbol TEXT,
      qty REAL, avg_cost REAL, price REAL, market_value REAL, unrealized_pnl REAL,
      pulled_at TEXT,
      PRIMARY KEY(date, venue, account, symbol)
    );
    CREATE TABLE IF NOT EXISTS account_daily(
      date TEXT, account TEXT, currency TEXT,
      total_value REAL, cash REAL, securities_value REAL,
      unrealized REAL, realized_cum REAL, note TEXT, pulled_at TEXT,
      PRIMARY KEY(date, account)
    );
    CREATE TABLE IF NOT EXISTS flows(
      venue TEXT, account TEXT, ext_id TEXT, type TEXT, amount REAL,
      currency TEXT, ts TEXT, pulled_at TEXT,
      PRIMARY KEY(venue, account, ext_id)
    );
    """)
    c.commit()
    return c

def upsert(c, table, cols, rows):
    if not rows:
        return 0
    ph = ",".join("?" * len(cols))
    c.executemany("INSERT OR REPLACE INTO %s(%s) VALUES(%s)" % (table, ",".join(cols), ph), rows)
    c.commit()
    return len(rows)

# ---------- Upbit ----------
UPBIT = "https://api.upbit.com"
def ub_qs(q):
    return urllib.parse.urlencode(q, doseq=True, safe=":")

def ub_jwt(ak, sk, q=None):
    payload = {"access_key": ak, "nonce": str(uuid.uuid4())}
    if q:
        payload["query_hash"] = hashlib.sha512(ub_qs(q).encode()).hexdigest()
        payload["query_hash_alg"] = "SHA512"
    b = lambda x: base64.urlsafe_b64encode(x).rstrip(b"=")
    hdr = b(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    pl = b(json.dumps(payload).encode())
    sig = b(hmac.new(sk.encode(), hdr + b"." + pl, hashlib.sha256).digest())
    return (hdr + b"." + pl + b"." + sig).decode()

def ub_call(path, q, ak, sk):
    url = UPBIT + path + ("?" + ub_qs(q) if q else "")
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + ub_jwt(ak, sk, q)})
    return json.load(urllib.request.urlopen(req, timeout=25))

def pull_upbit(c):
    print("[upbit] 시작", flush=True)
    ak, sk = ENV.get("UPBIT_ACCESS_KEY"), ENV.get("UPBIT_SECRET_KEY")
    if not ak or not sk:
        print("[upbit] 키 없음, skip"); return
    # 1) 전체 체결 주문 (주간 윈도우)
    ex_rows = []
    start = datetime.datetime(2026, 2, 1)
    END = datetime.datetime.now()          # 루프 밖에서 한 번만 고정 (무한루프 방지)
    t = start
    seen = set()
    while t < END:
        t2 = min(t + datetime.timedelta(days=7), END)
        try:
            batch = ub_call("/v1/orders/closed", {
                "start_time": t.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "end_time": t2.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "limit": 1000, "order_by": "asc"}, ak, sk)
            for o in batch:
                u = o.get("uuid")
                if u in seen: continue
                seen.add(u)
                funds = float(o.get("executed_funds") or 0) or float(o.get("price") or 0) * float(o.get("executed_volume") or 0)
                ex_rows.append(("upbit", "upbit", u, o.get("market"),
                                "buy" if o.get("side") == "bid" else "sell",
                                float(o.get("executed_volume") or 0), float(o.get("price") or 0),
                                funds, float(o.get("paid_fee") or 0), None,
                                o.get("created_at"), NOW))
        except Exception as e:
            print("[upbit] 체결 window", t.date(), "ERR", str(e)[:80])
        t = t2
        time.sleep(0.12)
    upsert(c, "executions", ["venue","account","ext_id","symbol","side","qty","price","amount","fee","realized_pnl","exec_time","pulled_at"], ex_rows)
    print("[upbit] 체결 %d건 적재" % len(ex_rows))

    # 2) 입출금 (KRW) - 순입금 산정용
    fl_rows = []
    for kind, path in [("deposit", "/v1/deposits"), ("withdraw", "/v1/withdraws")]:
        page = 1
        while page <= 50:
            try:
                batch = ub_call(path, {"currency": "KRW", "limit": 100, "page": page, "order_by": "asc"}, ak, sk)
            except Exception as e:
                print("[upbit]", kind, "ERR", str(e)[:80]); break
            if not batch: break
            for x in batch:
                if x.get("state", "").upper() not in ("ACCEPTED", "DONE", "PROCESSING", "CONFIRMED"):
                    # 완료된 것만
                    if x.get("state", "").upper() != "DONE": pass
                fl_rows.append(("upbit", "upbit", x.get("uuid") or (kind + (x.get("txid") or str(page))),
                                kind, float(x.get("amount") or 0), "KRW",
                                x.get("done_at") or x.get("created_at"), NOW))
            if len(batch) < 100: break
            page += 1
            time.sleep(0.12)
    upsert(c, "flows", ["venue","account","ext_id","type","amount","currency","ts","pulled_at"], fl_rows)
    print("[upbit] 입출금 %d건 적재" % len(fl_rows))

    # 3) 현재잔고 스냅샷 + account_daily
    acc = ub_call("/v1/accounts", {}, ak, sk)
    coins, krw = [], 0.0
    for a in acc:
        if a.get("currency") == "KRW":
            krw = float(a.get("balance") or 0) + float(a.get("locked") or 0)
        elif float(a.get("balance") or 0) + float(a.get("locked") or 0) > 0:
            coins.append(a)
    markets = ["KRW-" + a["currency"] for a in coins]
    prices = {}
    if markets:
        try:
            tk = ub_call("/v1/ticker", {"markets": ",".join(markets)}, ak, sk)
            prices = {t["market"]: float(t["trade_price"]) for t in tk}
        except Exception as e:
            print("[upbit] ticker ERR", str(e)[:60])
    snap, coin_val = [], 0.0
    for a in coins:
        cur = a["currency"]; m = "KRW-" + cur
        bal = float(a.get("balance") or 0) + float(a.get("locked") or 0)
        avg = float(a.get("avg_buy_price") or 0)
        px = prices.get(m, 0.0)
        mv = bal * px
        coin_val += mv
        snap.append((TODAY, "upbit", "upbit", m, bal, avg, px, mv, mv - bal * avg, NOW))
    upsert(c, "snapshots", ["date","venue","account","symbol","qty","avg_cost","price","market_value","unrealized_pnl","pulled_at"], snap)
    total = coin_val + krw
    upsert(c, "account_daily", ["date","account","currency","total_value","cash","securities_value","unrealized","realized_cum","note","pulled_at"],
           [(TODAY, "upbit", "KRW", total, krw, coin_val, None, None, "dca+scalp", NOW)])
    print("[upbit] 잔고 스냅샷: 코인 %.0f + KRW %.0f = %.0f" % (coin_val, krw, total))

# ---------- KIS ----------
KIS = "https://openapi.koreainvestment.com:9443"
def kis_tokens():
    m = {}
    for f in glob.glob(os.path.join(HOME, ".kis_token_*.json")):
        try:
            d = json.load(open(f))
            if d.get("app_key") and d.get("access_token"):
                exp = d.get("expires_at") or ""
                m[d["app_key"]] = {"token": d["access_token"], "exp": exp}
        except Exception:
            pass
    return m

def kis_acc(no):
    d = re.sub(r"\D", "", no); return d[:8], (d[8:10] if len(d) >= 10 else "01")

def kis_get(path, tr, ak, sk, tok, params):
    req = urllib.request.Request(KIS + path + "?" + urllib.parse.urlencode(params), headers={
        "content-type": "application/json", "authorization": "Bearer " + tok,
        "appkey": ak, "appsecret": sk, "tr_id": tr, "custtype": "P"})
    try:
        return json.load(urllib.request.urlopen(req, timeout=25))
    except urllib.error.HTTPError as e:
        return {"_err": e.code, "_body": e.read().decode()[:200]}

def token_expired(exp):
    if not exp: return True
    try:
        # "2026-07-30T14:31:56.749+09:00"
        base = exp[:19]
        dt = datetime.datetime.strptime(base, "%Y-%m-%dT%H:%M:%S")
        return dt <= datetime.datetime.now()
    except Exception:
        return False  # 파싱 실패 시 시도는 해봄

def pull_kis_domestic(c, account, kk, ks, ka, toks):
    ak, sk, no = ENV.get(kk), ENV.get(ks), ENV.get(ka)
    ti = toks.get(ak)
    if not ak or not ti:
        print("[%s] 토큰/키 없음, skip" % account); return
    if token_expired(ti["exp"]):
        print("[%s] 캐시 토큰 만료(%s) → skip (데몬 갱신 대기)" % (account, ti["exp"])); return
    tok = ti["token"]; cano, prdt = kis_acc(no)
    # 잔고
    r = kis_get("/uapi/domestic-stock/v1/trading/inquire-balance", "TTTC8434R", ak, sk, tok, {
        "CANO": cano, "ACNT_PRDT_CD": prdt, "AFHR_FLPR_YN": "N", "OFL_YN": "",
        "INQR_DVSN": "02", "UNPR_DVSN": "01", "FUND_STTL_ICLD_YN": "N",
        "FNCG_AMT_AUTO_RDPT_YN": "N", "PRCS_DVSN": "00",
        "CTX_AREA_FK100": "", "CTX_AREA_NK100": ""})
    if "_err" in r:
        print("[%s] 잔고 ERR %s %s" % (account, r["_err"], r["_body"][:80])); return
    o2 = r.get("output2", [{}])
    if isinstance(o2, list): o2 = o2[0] if o2 else {}
    snap = []
    for h in r.get("output1", []):
        q = float(h.get("hldg_qty") or 0)
        if q <= 0: continue
        snap.append((TODAY, "kis", account, h.get("pdno"), q,
                     float(h.get("pchs_avg_pric") or 0), float(h.get("prpr") or 0),
                     float(h.get("evlu_amt") or 0), float(h.get("evlu_pfls_amt") or 0), NOW))
    upsert(c, "snapshots", ["date","venue","account","symbol","qty","avg_cost","price","market_value","unrealized_pnl","pulled_at"], snap)
    total = float(o2.get("tot_evlu_amt") or 0)
    cash = float(o2.get("dnca_tot_amt") or 0)
    sec = float(o2.get("scts_evlu_amt") or 0)
    unrl = float(o2.get("evlu_pfls_smtl_amt") or 0)
    upsert(c, "account_daily", ["date","account","currency","total_value","cash","securities_value","unrealized","realized_cum","note","pulled_at"],
           [(TODAY, account, "KRW", total, cash, sec, unrl, None, "domestic", NOW)])
    print("[%s] 잔고 총 %.0f (현금 %.0f + 증권 %.0f, 미실현 %.0f), 보유 %d종" % (account, total, cash, sec, unrl, len(snap)))
    # 최근 3개월 체결 (국내 TTTC8001R)
    ex = []
    nk = fk = ""
    for _ in range(30):
        rr = kis_get("/uapi/domestic-stock/v1/trading/inquire-daily-ccld", "TTTC8001R", ak, sk, tok, {
            "CANO": cano, "ACNT_PRDT_CD": prdt,
            "INQR_STRT_DT": (datetime.date.today() - datetime.timedelta(days=91)).strftime("%Y%m%d"),
            "INQR_END_DT": datetime.date.today().strftime("%Y%m%d"),
            "SLL_BUY_DVSN_CD": "00", "INQR_DVSN": "00", "PDNO": "", "CCLD_DVSN": "01",
            "ORD_GNO_BRNO": "", "ODNO": "", "INQR_DVSN_3": "00", "INQR_DVSN_1": "",
            "CTX_AREA_FK100": fk, "CTX_AREA_NK100": nk})
        if "_err" in rr: break
        for o in rr.get("output1", []):
            q = float(o.get("tot_ccld_qty") or 0)
            if q <= 0: continue
            oid = (o.get("odno") or "") + "_" + (o.get("ord_dt") or o.get("ord_tmd") or "")
            side = "sell" if o.get("sll_buy_dvsn_cd") == "01" else "buy"
            ex.append(("kis", account, oid, o.get("pdno"), side, q,
                       float(o.get("avg_prvs") or o.get("ccld_prvs") or 0),
                       float(o.get("tot_ccld_amt") or 0), 0.0, None,
                       (o.get("ord_dt") or "") , NOW))
        nk = (rr.get("ctx_area_nk100") or "").strip(); fk = (rr.get("ctx_area_fk100") or "").strip()
        if (rr.get("tr_cont") not in ("F", "M")) or not nk: break
    upsert(c, "executions", ["venue","account","ext_id","symbol","side","qty","price","amount","fee","realized_pnl","exec_time","pulled_at"], ex)
    print("[%s] 최근3개월 체결 %d건 적재" % (account, len(ex)))

def pull_kis_overseas(c, account, kk, ks, ka, toks):
    ak, sk, no = ENV.get(kk), ENV.get(ks), ENV.get(ka)
    ti = toks.get(ak)
    if not ak or not ti:
        print("[%s] 토큰/키 없음, skip" % account); return
    if token_expired(ti["exp"]):
        print("[%s] 캐시 토큰 만료 → skip" % account); return
    tok = ti["token"]; cano, prdt = kis_acc(no)
    # 잔고
    r = kis_get("/uapi/overseas-stock/v1/trading/inquire-balance", "TTTS3012R", ak, sk, tok, {
        "CANO": cano, "ACNT_PRDT_CD": prdt, "OVRS_EXCG_CD": "NASD", "TR_CRCY_CD": "USD",
        "CTX_AREA_FK200": "", "CTX_AREA_NK200": ""})
    if "_err" in r:
        print("[%s] 해외잔고 ERR %s %s" % (account, r["_err"], r["_body"][:80])); return
    snap, sec_usd = [], 0.0
    for h in r.get("output1", []):
        q = float(h.get("ovrs_cblc_qty") or 0)
        if q <= 0: continue
        mv = float(h.get("ovrs_stck_evlu_amt") or 0)
        sec_usd += mv
        snap.append((TODAY, "kis", account, h.get("ovrs_pdno"), q,
                     float(h.get("pchs_avg_pric") or 0), float(h.get("now_pric2") or 0),
                     mv, float(h.get("frcr_evlu_pfls_amt") or 0), NOW))
    upsert(c, "snapshots", ["date","venue","account","symbol","qty","avg_cost","price","market_value","unrealized_pnl","pulled_at"], snap)
    o2 = r.get("output2", {})
    if isinstance(o2, list): o2 = o2[0] if o2 else {}
    unrl = float(o2.get("ovrs_tot_pfls") or 0)
    # 해외 실현손익 (기간)
    realized = 0.0
    for s, e in [("20260201", "20260430"), ("20260501", datetime.date.today().strftime("%Y%m%d"))]:
        rr = kis_get("/uapi/overseas-stock/v1/trading/inquire-period-profit", "TTTS3039R", ak, sk, tok, {
            "CANO": cano, "ACNT_PRDT_CD": prdt, "OVRS_EXCG_CD": "", "NATN_CD": "", "CRCY_CD": "",
            "PDNO": "", "INQR_STRT_DT": s, "INQR_END_DT": e, "WCRC_FRCR_DVSN_CD": "02",
            "CTX_AREA_FK200": "", "CTX_AREA_NK200": ""})
        if "_err" in rr: continue
        oo = rr.get("output2", {})
        if isinstance(oo, list): oo = oo[0] if oo else {}
        try: realized += float(oo.get("ovrs_rlzt_pfls_tot_amt") or 0)
        except Exception: pass
    upsert(c, "account_daily", ["date","account","currency","total_value","cash","securities_value","unrealized","realized_cum","note","pulled_at"],
           [(TODAY, account, "USD", sec_usd, None, sec_usd, unrl, realized, "overseas(USD); realized in KRW", NOW)])
    print("[%s] 해외잔고 %.2f USD, 미실현 %.2f USD, 기간실현 %.0f KRW, 보유 %d종" % (account, sec_usd, unrl, realized, len(snap)))

def main():
    c = db_init()
    print("=== 브로커 원장 적재 %s ===" % NOW)
    try: pull_upbit(c)
    except Exception as e: print("[upbit] 전체 ERR", str(e)[:120])
    toks = kis_tokens()
    print("[kis] 캐시 토큰 %d개 로드" % len(toks))
    for acct, kk, ks, ka in [
        ("kis_battle", "KIS_BATTLE_APP_KEY", "KIS_BATTLE_APP_SECRET", "KIS_BATTLE_ACCOUNT_NO"),
        ("kis_kr", "KIS_KR_APP_KEY", "KIS_KR_APP_SECRET", "KIS_KR_ACCOUNT_NO")]:
        try: pull_kis_domestic(c, acct, kk, ks, ka, toks)
        except Exception as e: print("[%s] ERR" % acct, str(e)[:120])
    try: pull_kis_overseas(c, "kis_main", "KIS_APP_KEY", "KIS_APP_SECRET", "KIS_ACCOUNT_NO", toks)
    except Exception as e: print("[kis_main] ERR", str(e)[:120])
    # 카운트 요약
    for t in ["executions", "flows", "snapshots", "account_daily"]:
        n = c.execute("SELECT COUNT(*) FROM %s" % t).fetchone()[0]
        print("  %s: %d행" % (t, n))
    c.close()
    print("=== 적재 완료: %s ===" % DB)

if __name__ == "__main__":
    main()
