package apiv1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// 쓰기 API 의 요청·응답 표현(명세 10절). 쓰기는 거래처·품목 가져오기(미리보기 → 반영)와 전표 초안뿐이다(D10).
// 필드 이름은 snake_case 고정이다. OpenAPI 스키마는 이 구조체에서 생성한다(D14).

// Row 는 가져오기 행 하나다: 키는 양식 열의 식별자(거래처 name·business_no·payment_days·phone·email·account_no, 품목 sku·name·unit·safety_stock·lot_tracked)이고
// 값은 문자열이다. JSON 숫자·불리언도 받아 글자로 바꾼다(양식 파일과 같은 검증을 거친다).
type Row map[string]string

// UnmarshalJSON 은 문자열·숫자·불리언·null 값을 받는다. 객체·배열은 거부한다.
func (r *Row) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("행은 {\"열 이름\": 값} 모양이어야 합니다")
	}
	out := make(Row, len(raw))
	for k, v := range raw {
		switch x := v.(type) {
		case nil:
		case string:
			out[k] = x
		case json.Number:
			out[k] = x.String()
		case bool:
			if x {
				out[k] = "예"
			} else {
				out[k] = "아니오"
			}
		default:
			return fmt.Errorf("%q 의 값은 문자열·숫자·불리언이어야 합니다", k)
		}
	}
	*r = out
	return nil
}

// ImportRequest 는 가져오기 미리보기 요청이다.
type ImportRequest struct {
	Rows []Row `json:"rows" doc:"가져올 행(최대 5,000). 키는 양식 열 식별자"`
}

// ImportApplyRequest 는 가져오기 반영 요청이다. 미리보기와 같은 행을 다시 보내고 미리보기가 준 반영 토큰을 함께 보낸다.
type ImportApplyRequest struct {
	ApplyToken string `json:"apply_token" doc:"미리보기 응답의 apply_token(일회용)"`
	Rows       []Row  `json:"rows" doc:"미리보기와 같은 행"`
}

// ImportCounts 는 건수다.
type ImportCounts struct {
	Total  int `json:"total" doc:"전체 행"`
	Add    int `json:"add" doc:"추가될 행"`
	Same   int `json:"same" doc:"이미 같은 행"`
	Change int `json:"change" doc:"바뀔 행"`
	Error  int `json:"error" doc:"오류 행"`
	Warn   int `json:"warn" doc:"경고가 붙은 행"`
}

// ImportChange 는 바뀌는 칸 하나다(민감한 값은 가린 값).
type ImportChange struct {
	Column string `json:"column" doc:"열 이름"`
	Old    string `json:"old" doc:"이전 값(민감 열은 가린 값)"`
	New    string `json:"new" doc:"새 값(민감 열은 가린 값)"`
}

// ImportRowResult 는 행 하나의 판정이다.
type ImportRowResult struct {
	RowNo   int            `json:"row_no" doc:"보낸 행의 순번(1부터가 아니라 양식의 행 번호: 첫 데이터 행이 2)"`
	Key     string         `json:"key" doc:"행을 구분하는 키(민감한 키는 가린 값)"`
	Verdict string         `json:"verdict" doc:"add | change | same | error"`
	Reason  string         `json:"reason,omitempty" doc:"오류 사유(한국어)"`
	Warn    string         `json:"warn,omitempty" doc:"경고 문구(한국어)"`
	Changes []ImportChange `json:"changes,omitempty" doc:"바뀌는 칸"`
}

// ImportPreview 는 미리보기 결과다. 아무것도 반영하지 않는다.
type ImportPreview struct {
	BatchNo       string            `json:"batch_no" doc:"가져오기 묶음 번호(전부 같은 행이면 빈 값)"`
	ApplyToken    string            `json:"apply_token,omitempty" doc:"반영 토큰. 이 응답에서만 보인다(일회용, 24시간). 반영할 수 없으면 없다"`
	Target        string            `json:"target" doc:"vendors | items"`
	Counts        ImportCounts      `json:"counts"`
	Applicable    bool              `json:"applicable" doc:"오류 0건이고 반영할 변경이 있다"`
	Message       string            `json:"message,omitempty" doc:"반영할 수 없을 때의 한국어 안내"`
	Rows          []ImportRowResult `json:"rows" doc:"오류·경고·변경·추가 순으로 최대 200행"`
	RowsTruncated bool              `json:"rows_truncated" doc:"더 많은 행이 있다"`
}

// ImportApplied 는 반영 결과다.
type ImportApplied struct {
	BatchNo   string       `json:"batch_no" doc:"가져오기 묶음 번호"`
	Target    string       `json:"target" doc:"vendors | items"`
	Status    string       `json:"status" doc:"applied"`
	Counts    ImportCounts `json:"counts"`
	AppliedAt string       `json:"applied_at,omitempty" doc:"반영 시각(RFC3339 UTC)"`
}

// JournalDraftLine 은 전표 초안의 줄이다.
type JournalDraftLine struct {
	AccountCode string `json:"account_code" doc:"계정 코드(4자리)"`
	Side        string `json:"side" doc:"D(차변) | C(대변)"`
	Amount      int64  `json:"amount" doc:"금액(원, 1 이상)"`
	VendorID    int64  `json:"vendor_id,omitempty" doc:"거래처 번호(외상매입금 줄은 필수)"`
	Memo        string `json:"memo,omitempty" doc:"줄 적요(200자 이내)"`
}

// JournalDraftRequest 는 전표 초안 제출 요청이다.
type JournalDraftRequest struct {
	PostingDate string             `json:"posting_date" doc:"전기일(YYYY-MM-DD). 마감된 달이면 거부된다"`
	TradeDate   string             `json:"trade_date,omitempty" doc:"거래일(YYYY-MM-DD, 비우면 전기일)"`
	Memo        string             `json:"memo" doc:"적요(1~200자)"`
	ExternalRef string             `json:"external_ref" doc:"호출자가 정하는 1~64자 식별자. 같은 토큰의 같은 값은 멱등이다(내용이 다르면 409)"`
	Lines       []JournalDraftLine `json:"lines" doc:"전표 줄(2개 이상, 차변 합계 = 대변 합계)"`
}

// JournalDraft 는 만들어진(또는 이미 있던) 전표 초안이다. 승인은 API 로 할 수 없다: 재무가 화면에서 승인을 요청한다.
type JournalDraft struct {
	No          string `json:"no" doc:"전표 번호"`
	Status      string `json:"status" doc:"draft"`
	PostingDate string `json:"posting_date" doc:"전기일"`
	TradeDate   string `json:"trade_date" doc:"거래일"`
	TotalAmount int64  `json:"total_amount" doc:"합계 금액(원)"`
	ExternalRef string `json:"external_ref" doc:"보낸 external_ref"`
}

// WriteMeta 는 쓰기 결과의 전송 정보다.
type WriteMeta struct {
	Status   int  // 응답 상태 코드(생성 201, 이미 있음·재생 200)
	Replayed bool // Idempotency-Key 재요청이라 첫 응답을 되돌려 줬다
}

// Writer 는 쓰기 라우트의 서비스 쪽이다(구현: internal/apiwrite). 서비스 층의 같은 함수(가져오기 흐름·전표 서비스)를 거치고 상태 전이·감사·SoD 를 우회하지 않는다.
// 한도(분당 쓰기)·멱등·감사는 구현이 맡는다. key 는 Idempotency-Key(비면 없음), hash 는 요청 본문의 해시(같은 키의 다른 본문 감지)다.
type Writer interface {
	PreviewImport(ctx context.Context, c Caller, target string, rows []Row) (ImportPreview, WriteMeta, error)
	ApplyImport(ctx context.Context, c Caller, key, hash, target, batchNo string, req ImportApplyRequest) (ImportApplied, WriteMeta, error)
	DraftJournal(ctx context.Context, c Caller, key, hash string, req JournalDraftRequest) (JournalDraft, WriteMeta, error)
}
