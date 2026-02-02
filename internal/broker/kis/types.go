package kis

// Credentials KIS API 인증 정보
type Credentials struct {
	AppKey    string
	AppSecret string
	AccountNo string // XXXXXXXX-XX 형식
}

// 해외주식 거래 ID (실전투자)
const (
	TrIDBuyReal       = "JTTT1002U" // 해외주식 매수
	TrIDSellReal      = "JTTT1006U" // 해외주식 매도
	TrIDCancelReal    = "JTTT1004U" // 해외주식 정정/취소
	TrIDBalanceReal   = "JTTT3012R" // 해외주식 잔고조회
	TrIDPendingReal   = "JTTT3018R" // 미체결 조회
	TrIDOrderReal     = "JTTT3001R" // 주문내역 조회
	TrIDPriceReal     = "HHDFS00000300" // 해외주식 현재가
	TrIDBuyingPower   = "JTTT3007R" // 해외주식 매수가능금액조회
)

// 거래소 코드
const (
	ExchangeNYSE   = "NYSE" // 뉴욕
	ExchangeNASDAQ = "NASD" // 나스닥
	ExchangeAMEX   = "AMEX" // 아멕스
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
		OVRS_PDNO        string `json:"ovrs_pdno"`         // 종목코드
		OVRS_ITEM_NAME   string `json:"ovrs_item_name"`    // 종목명
		CBLC_QTY13       string `json:"cblc_qty13"`        // 보유수량
		PCHS_AVG_PRIC    string `json:"pchs_avg_pric"`     // 평균매입가
		OVRS_STCK_EVLU_AMT string `json:"ovrs_stck_evlu_amt"` // 평가금액
		EVLU_PFLS_AMT    string `json:"evlu_pfls_amt"`     // 평가손익
		NOW_PRIC2        string `json:"now_pric2"`         // 현재가
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
		OVRS_PDNO        string `json:"ovrs_pdno"`        // 종목코드
		SLL_BUY_DVSN_CD  string `json:"sll_buy_dvsn_cd"`  // 매도매수구분 ("01"=매도, "02"=매수)
		ORD_QTY          string `json:"ord_qty"`          // 주문수량
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
