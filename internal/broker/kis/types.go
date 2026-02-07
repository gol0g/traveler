package kis

// Credentials KIS API 인증 정보
type Credentials struct {
	AppKey    string
	AppSecret string
	AccountNo string // XXXXXXXX-XX 형식
}

// 해외주식 거래 ID (실전투자)
// T로 시작 = 해외주식, J로 시작 = 국내주식
const (
	TrIDBuyReal       = "TTTT1002U" // 해외주식 매수 (미국)
	TrIDSellReal      = "TTTT1006U" // 해외주식 매도 (미국)
	TrIDCancelReal    = "TTTT1004U" // 해외주식 정정/취소
	TrIDBalanceReal   = "TTTS3012R" // 해외주식 잔고조회
	TrIDPendingReal   = "TTTS3018R" // 미체결 조회
	TrIDOrderReal     = "TTTS3001R" // 주문내역 조회
	TrIDPriceReal     = "HHDFS00000300" // 해외주식 현재가
	TrIDBuyingPower   = "TTTS3007R" // 해외주식 매수가능금액조회
)

// 국내주식 거래 ID (실전투자)
const (
	TrIDDomBuyReal     = "TTTC0802U"     // 국내 매수
	TrIDDomSellReal    = "TTTC0801U"     // 국내 매도
	TrIDDomBalanceReal = "TTTC8434R"     // 국내 잔고조회
	TrIDDomPendingReal = "TTTC8036R"     // 국내 미체결조회
	TrIDDomPriceReal   = "FHKST01010100" // 국내 현재가
	TrIDDomCandleReal  = "FHKST03010100" // 국내 일봉
	TrIDDomBuyPower    = "TTTC8908R"     // 국내 매수가능금액
)

// 거래소 코드 (KIS API용)
const (
	ExchangeNYSE   = "NYS" // 뉴욕
	ExchangeNASDAQ = "NAS" // 나스닥
	ExchangeAMEX   = "AMS" // 아멕스
)

// tokenRequest 토큰 발급 요청
type tokenRequest struct {
	GrantType string `json:"grant_type"`
	AppKey    string `json:"appkey"`
	AppSecret string `json:"appsecret"`
}

// tokenResponse 토큰 발급 응답
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"` // 초 (86400 = 24시간)
}

// orderRequest 주문 요청
type orderRequest struct {
	CANO           string `json:"CANO"`            // 계좌번호 앞 8자리
	ACNT           string `json:"ACNT_PRDT_CD"`    // 계좌상품코드 (뒤 2자리)
	OVRS_EXCG_CD   string `json:"OVRS_EXCG_CD"`    // 해외거래소코드
	PDNO           string `json:"PDNO"`            // 종목코드
	ORD_QTY        string `json:"ORD_QTY"`         // 주문수량
	OVRS_ORD_UNPR  string `json:"OVRS_ORD_UNPR"`   // 주문단가 (시장가=0)
	ORD_SVR_DVSN_CD string `json:"ORD_SVR_DVSN_CD"` // 주문서버구분코드 ("0")
	ORD_DVSN       string `json:"ORD_DVSN"`        // 주문구분 ("00"=지정가, "01"=시장가)
}

// orderResponse 주문 응답
type orderResponse struct {
	RtCd   string `json:"rt_cd"` // 성공: "0"
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output struct {
		ODNO   string `json:"ODNO"`    // 주문번호
		ORDTM  string `json:"ORD_TMD"` // 주문시각
	} `json:"output"`
}

// cancelRequest 주문 취소 요청
type cancelRequest struct {
	CANO           string `json:"CANO"`
	ACNT           string `json:"ACNT_PRDT_CD"`
	OVRS_EXCG_CD   string `json:"OVRS_EXCG_CD"`
	PDNO           string `json:"PDNO"`
	ORGN_ODNO      string `json:"ORGN_ODNO"`      // 원주문번호
	RVSE_CNCL_DVSN_CD string `json:"RVSE_CNCL_DVSN_CD"` // "02"=취소
	ORD_QTY        string `json:"ORD_QTY"`
	OVRS_ORD_UNPR  string `json:"OVRS_ORD_UNPR"`
	ORD_SVR_DVSN_CD string `json:"ORD_SVR_DVSN_CD"`
}

// balanceResponse 잔고 조회 응답
type balanceResponse struct {
	RtCd    string `json:"rt_cd"`
	MsgCd   string `json:"msg_cd"`
	Msg1    string `json:"msg1"`
	Output1 []struct {
		OVRS_PDNO          string `json:"ovrs_pdno"`           // 종목코드
		OVRS_ITEM_NAME     string `json:"ovrs_item_name"`      // 종목명
		OVRS_CBLC_QTY      string `json:"ovrs_cblc_qty"`       // 보유수량
		PCHS_AVG_PRIC      string `json:"pchs_avg_pric"`       // 평균매입가
		OVRS_STCK_EVLU_AMT string `json:"ovrs_stck_evlu_amt"`  // 평가금액
		FRCR_EVLU_PFLS_AMT string `json:"frcr_evlu_pfls_amt"`  // 평가손익
		NOW_PRIC2          string `json:"now_pric2"`           // 현재가
	} `json:"output1"`
	Output2 struct {
		FRCR_PCHS_AMT1    string `json:"frcr_pchs_amt1"`    // 외화매수금액
		OVRS_TOT_PFLS     string `json:"ovrs_tot_pfls"`     // 해외총손익
		TOT_EVLU_PFLS_AMT string `json:"tot_evlu_pfls_amt"` // 총평가손익
		FRCR_EVLU_AMT2    string `json:"frcr_evlu_amt2"`    // 외화평가금액
		EVLU_ERNG_RT1     string `json:"evlu_erng_rt1"`     // 평가수익률
	} `json:"output2"`
}

// pendingResponse 미체결 조회 응답
type pendingResponse struct {
	RtCd    string `json:"rt_cd"`
	MsgCd   string `json:"msg_cd"`
	Msg1    string `json:"msg1"`
	Output  []struct {
		ODNO             string `json:"odno"`             // 주문번호
		PDNO             string `json:"pdno"`             // 종목코드
		SLL_BUY_DVSN_CD  string `json:"sll_buy_dvsn_cd"`  // 매도매수구분 ("01"=매도, "02"=매수)
		FT_ORD_QTY       string `json:"ft_ord_qty"`       // 주문수량
		NCCS_QTY         string `json:"nccs_qty"`         // 미체결수량
		FT_ORD_UNPR3     string `json:"ft_ord_unpr3"`     // 주문단가
		ORD_TMD          string `json:"ord_tmd"`          // 주문시각
	} `json:"output"`
}

// priceResponse 현재가 조회 응답
type priceResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output struct {
		LAST string `json:"last"` // 현재가
		DIFF string `json:"diff"` // 전일대비
		RATE string `json:"rate"` // 등락률
	} `json:"output"`
}

// apiError API 에러 응답
type apiError struct {
	RtCd  string `json:"rt_cd"`
	MsgCd string `json:"msg_cd"`
	Msg1  string `json:"msg1"`
}

// buyingPowerResponse 매수가능금액 조회 응답
type buyingPowerResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output struct {
		ORD_PSBL_FRCR_AMT  string `json:"ord_psbl_frcr_amt"`  // 외화주문가능금액 (USD)
		OVRS_ORD_PSBL_AMT  string `json:"ovrs_ord_psbl_amt"`  // 해외주문가능금액
		FRCR_ORD_PSBL_AMT1 string `json:"frcr_ord_psbl_amt1"` // (참고용)
		MAX_ORD_PSBL_QTY   string `json:"max_ord_psbl_qty"`   // 최대주문가능수량
		EXRT               string `json:"exrt"`               // 환율
	} `json:"output"`
}

// ========== 국내주식 타입 ==========

// Market 시장 구분
type Market string

const (
	MarketOverseas Market = "overseas"
	MarketDomestic Market = "domestic"
)

// domOrderRequest 국내 주문 요청
type domOrderRequest struct {
	CANO     string `json:"CANO"`         // 계좌번호 앞 8자리
	ACNT     string `json:"ACNT_PRDT_CD"` // 계좌상품코드 (뒤 2자리)
	PDNO     string `json:"PDNO"`         // 종목코드 (6자리)
	ORD_DVSN string `json:"ORD_DVSN"`     // "00"=지정가, "01"=시장가
	ORD_QTY  string `json:"ORD_QTY"`      // 주문수량
	ORD_UNPR string `json:"ORD_UNPR"`     // 주문단가 (정수, 시장가=0)
}

// domBalanceResponse 국내 잔고조회 응답 (TTTC8434R)
type domBalanceResponse struct {
	RtCd    string `json:"rt_cd"`
	MsgCd   string `json:"msg_cd"`
	Msg1    string `json:"msg1"`
	Output1 []struct {
		PDNO           string `json:"pdno"`            // 종목코드
		PRDT_NAME      string `json:"prdt_name"`       // 종목명
		HLDG_QTY       string `json:"hldg_qty"`        // 보유수량
		PCHS_AVG_PRIC  string `json:"pchs_avg_pric"`   // 매입평균가
		PRPR           string `json:"prpr"`             // 현재가
		EVLU_AMT       string `json:"evlu_amt"`         // 평가금액
		EVLU_PFLS_AMT  string `json:"evlu_pfls_amt"`    // 평가손익
		EVLU_PFLS_RT   string `json:"evlu_pfls_rt"`     // 평가수익률
		EVLU_ERNG_RT   string `json:"evlu_erng_rt"`     // 평가수익률(%)
	} `json:"output1"`
	Output2 []struct {
		DNCA_TOT_AMT      string `json:"dnca_tot_amt"`       // 예수금총금액
		NXDY_EXCC_AMT     string `json:"nxdy_excc_amt"`      // 익일정산금액
		PRVS_RCDL_EXCC_AMT string `json:"prvs_rcdl_excc_amt"` // D+2 예수금
		SCTS_EVLU_AMT     string `json:"scts_evlu_amt"`       // 유가평가금액
		TOT_EVLU_AMT      string `json:"tot_evlu_amt"`        // 총평가금액
		BFDY_TOT_ASST_EVLU_AMT string `json:"bfdy_tot_asst_evlu_amt"` // 전일총자산평가
		PCHS_AMT_SMTL_AMT string `json:"pchs_amt_smtl_amt"`  // 매입금액합계
		EVLU_AMT_SMTL_AMT string `json:"evlu_amt_smtl_amt"`  // 평가금액합계
		EVLU_PFLS_SMTL_AMT string `json:"evlu_pfls_smtl_amt"` // 평가손익합계
	} `json:"output2"`
}

// domPriceResponse 국내 현재가 응답 (FHKST01010100)
type domPriceResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output struct {
		STCK_PRPR  string `json:"stck_prpr"`   // 현재가
		PRDY_VRSS string `json:"prdy_vrss"`    // 전일대비
		PRDY_CTRT string `json:"prdy_ctrt"`    // 전일대비율
		STCK_OPRC string `json:"stck_oprc"`    // 시가
		STCK_HGPR string `json:"stck_hgpr"`    // 고가
		STCK_LWPR string `json:"stck_lwpr"`    // 저가
		ACML_VOL  string `json:"acml_vol"`     // 누적거래량
		HTS_KOR_ISNM string `json:"hts_kor_isnm"` // 종목명
	} `json:"output"`
}

// domPendingResponse 국내 미체결 조회 응답 (TTTC8036R)
type domPendingResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output []struct {
		ODNO          string `json:"odno"`           // 주문번호
		PDNO          string `json:"pdno"`           // 종목코드
		SLL_BUY_DVSN_CD string `json:"sll_buy_dvsn_cd"` // "01"=매도, "02"=매수
		ORD_QTY       string `json:"ord_qty"`        // 주문수량
		RMNN_QTY      string `json:"rmnn_qty"`       // 잔여수량
		ORD_UNPR      string `json:"ord_unpr"`       // 주문단가
		ORD_TMD       string `json:"ord_tmd"`        // 주문시각
		PRDT_NAME     string `json:"prdt_name"`      // 종목명
	} `json:"output"`
}

// domCandleResponse 국내 일봉 응답 (FHKST03010100)
type domCandleResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output2 []struct {
		STCK_BSOP_DATE string `json:"stck_bsop_date"` // 영업일자 (YYYYMMDD)
		STCK_OPRC      string `json:"stck_oprc"`       // 시가
		STCK_HGPR      string `json:"stck_hgpr"`       // 고가
		STCK_LWPR      string `json:"stck_lwpr"`       // 저가
		STCK_CLPR      string `json:"stck_clpr"`       // 종가
		ACML_VOL       string `json:"acml_vol"`        // 누적거래량
	} `json:"output2"`
}

// domBuyPowerResponse 국내 매수가능금액 응답 (TTTC8908R)
type domBuyPowerResponse struct {
	RtCd   string `json:"rt_cd"`
	MsgCd  string `json:"msg_cd"`
	Msg1   string `json:"msg1"`
	Output struct {
		ORD_PSBL_CASH string `json:"ord_psbl_cash"` // 주문가능현금
		ORD_PSBL_SBST string `json:"ord_psbl_sbst"` // 주문가능대용
		RUSE_PSBL_AMT string `json:"ruse_psbl_amt"` // 재사용가능금액
		NRCVB_BUY_AMT string `json:"nrcvb_buy_amt"` // 미수없는매수금액
	} `json:"output"`
}
