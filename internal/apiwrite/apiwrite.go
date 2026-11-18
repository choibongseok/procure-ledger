// Package apiwrite 는 연동 API(B42 3단계, 10절)의 쓰기 서비스다.·ai.
//
// 쓰기는 거래처·품목 가져오기(미리보기 → 반영)와 전표 초안 제출뿐이다(D10). 이 패키지는 새 판정을 만들지 않는다:
//   - 가져오기는 JSON 행을 양식 XLSX 로 바꿔(importx.BuildXLSXFromRows) 화면과 같은 공통 흐름(domain.ImportService: 파서·대상 어댑터 Plan/Apply·일회용 반영 토큰·감사)에 넣는다.
//   - 전표 초안은 domain.APIJournalDrafter(JournalService.CreateDraftFromAPI)가 일반 전표와 같은 검증·월 마감 검사를 거쳐 만든다. 작성자는 토큰 발급자이고 승인은 B41 SoD 가 판정한다.
//   - 권한은 서비스가 호출자(Principal: 토큰 범위가 정한 Perms)로 판정한다. 이 패키지는 판정을 하지 않고 한도·멱등·감사만 맡는다.
//
// 한도·멱등·감사
//   - 쓰기 호출은 토큰마다 분당 api.write_per_min(기본 30)회까지다(읽기 한도와 따로, api_write_windows 분 창). 넘으면 429 와 Retry-After 이고 첫 거부만 감사에 남는다.
//   - Idempotency-Key(1~64자): (토큰, 키)로 24시간 첫 응답의 상태와 결과(값 없는 건수·번호)를 보존해 같은 본문의 재요청에 같은 응답을 준다. 다른 본문이면 409.
//   - 성공·실패(검증·충돌·거부) 모두 건마다 감사에 남는다. 감사 값에는 행 값·본문이 없고 대상·건수·묶음 번호·사유 코드만 있다.
package apiwrite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/importx"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// Options 는 Service 구성이다.
type Options struct {
	DB       *sql.DB
	Txs      contract.TxManager
	Audit    audit.Recorder
	Import   domain.ImportService
	Journals domain.APIJournalDrafter
	Now      func() time.Time
}

// Service 는 apiv1.Writer 의 구현이다.
type Service struct{ o Options }

var _ apiv1.Writer = (*Service)(nil)

// New 는 Service 를 만든다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

const (
	defaultWritePerMin = 30
	idemTTL            = 24 * time.Hour
)

// importTargets 는 API 경로의 대상 이름과 가져오기 흐름의 대상 코드다.
var importTargets = map[string]string{"vendors": domain.ImportVendor, "items": domain.ImportItem}

func (s *Service) now() time.Time { return s.o.Now().UTC() }

// ---- 감사 ----

// rec 는 감사 한 건을 짧은 별도 트랜잭션에 남긴다(요청의 성패와 무관하게 남기고, 실패는 로그만 남긴다).
func (s *Service) rec(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id string, result audit.Result, diff []audit.DiffItem, detail string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: tt, TargetID: id, Result: result, Diff: diff, Detail: detail})
	})
	if err != nil {
		log.Printf("apiwrite: 감사 기록 실패(%s): %v", action, err)
	}
}

// failed 는 실패한 쓰기를 감사에 남긴다: 거부(권한·범위)는 denied, 그 밖의 검증·충돌은 error. 서비스가 이미 거부 기록을 남긴 경우(권한·마감된 달)는 중복을 만들지 않는다.
func (s *Service) failed(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id string, target string, err error) {
	var closed *repository.MonthClosedError
	if errors.Is(err, contract.ErrForbidden) || errors.As(err, &closed) {
		return // 서비스 쓰기 래퍼가 denied 로 남겼다
	}
	reason := "error"
	switch {
	case errors.Is(err, contract.ErrValidation):
		reason = "invalid_argument"
	case errors.Is(err, contract.ErrInvalidState):
		reason = "conflict"
	case errors.Is(err, contract.ErrNotFound):
		reason = "not_found"
	}
	s.rec(ctx, p, action, tt, id, audit.ResultError, []audit.DiffItem{{Key: "api_reason", New: reason}, {Key: "target", New: target}}, "연동 API 쓰기 실패")
}

// ---- 분당 쓰기 한도 ----

func (s *Service) writeLimit(ctx context.Context) int {
	var v string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'api.write_per_min'`).Scan(&v); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return defaultWritePerMin
}

// throttle 은 이 호출을 분 창에 더하고 한도를 넘으면 apiv1.ErrRateLimited 를 돌려준다. 계수 실패는 쓰기를 막지 않는다(로그만).
func (s *Service) throttle(ctx context.Context, c apiv1.Caller) error {
	now := s.now()
	minute := now.Unix() / 60
	var calls int
	if err := s.o.DB.QueryRowContext(ctx, `INSERT INTO api_write_windows (token_id, minute, calls, denied) VALUES (?,?,1,0)
		ON CONFLICT (token_id, minute) DO UPDATE SET calls = api_write_windows.calls + 1 RETURNING calls`, c.TokenID, minute).Scan(&calls); err != nil {
		log.Printf("apiwrite: 쓰기 호출 수 기록 실패: %v", err)
		return nil
	}
	if calls <= s.writeLimit(ctx) {
		return nil
	}
	var denied int
	if err := s.o.DB.QueryRowContext(ctx, `UPDATE api_write_windows SET denied = denied + 1 WHERE token_id = ? AND minute = ? RETURNING denied`, c.TokenID, minute).Scan(&denied); err != nil {
		denied = 2
	}
	if denied == 1 { // 창의 첫 거부만 감사에 남긴다
		s.rec(ctx, c.Principal, "api.rate_limited", audit.TargetAPIToken, strconv.FormatInt(c.TokenID, 10), audit.ResultDenied, []audit.DiffItem{{Key: "api_reason", New: "write_rate_limited"}}, "분당 쓰기 한도 초과")
	}
	wait := int((minute+1)*60 - now.Unix())
	if wait < 1 {
		wait = 1
	}
	return apiv1.ErrRateLimited{RetryAfterSeconds: wait}
}

// ---- 멱등 ----

type idemRow struct {
	hash   string
	status int
	ref    string
}

// idemLookup 은 (토큰, 키)의 보존된 첫 응답을 찾는다. 24시간이 지난 행은 지우고 없는 것으로 본다.
func (s *Service) idemLookup(ctx context.Context, tokenID int64, key string) (idemRow, bool, error) {
	if key == "" {
		return idemRow{}, false, nil
	}
	var r idemRow
	var created string
	err := s.o.DB.QueryRowContext(ctx, `SELECT request_hash, status_code, result_ref, created_at FROM api_idempotency WHERE token_id = ? AND idem_key = ?`, tokenID, key).Scan(&r.hash, &r.status, &r.ref, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if t, perr := time.Parse(time.RFC3339, created); perr == nil && s.now().Sub(t) > idemTTL {
		_, derr := s.o.DB.ExecContext(ctx, `DELETE FROM api_idempotency WHERE token_id = ? AND idem_key = ?`, tokenID, key)
		return idemRow{}, false, derr
	}
	return r, true, nil
}

func (s *Service) idemStore(ctx context.Context, tokenID int64, key, hash string, status int, ref string) {
	if key == "" {
		return
	}
	_, err := s.o.DB.ExecContext(ctx, `INSERT INTO api_idempotency (token_id, idem_key, request_hash, status_code, result_ref, created_at) VALUES (?,?,?,?,?,?) ON CONFLICT (token_id, idem_key) DO NOTHING`,
		tokenID, key, hash, status, ref, s.now().Format(time.RFC3339))
	if err != nil {
		log.Printf("apiwrite: 멱등 키 기록 실패: %v", err)
	}
}

// idemReplay 는 보존된 응답이 있으면 그 결과 참조를 돌려준다. 같은 키의 다른 본문은 충돌이다.
func (s *Service) idemReplay(ctx context.Context, c apiv1.Caller, key, hash string, into any) (apiv1.WriteMeta, bool, error) {
	row, found, err := s.idemLookup(ctx, c.TokenID, key)
	if err != nil || !found {
		return apiv1.WriteMeta{}, false, err
	}
	if row.hash != hash {
		return apiv1.WriteMeta{}, false, fmt.Errorf("%w: 같은 Idempotency-Key 로 다른 내용의 요청이 이미 처리되었습니다", contract.ErrInvalidState)
	}
	if err := json.Unmarshal([]byte(row.ref), into); err != nil {
		return apiv1.WriteMeta{}, false, err
	}
	return apiv1.WriteMeta{Status: row.status, Replayed: true}, true, nil
}

// ---- 가져오기 ----

func (s *Service) importTarget(target string) (string, error) {
	t, ok := importTargets[target]
	if !ok || s.o.Import == nil {
		return "", contract.ErrNotFound
	}
	return t, nil
}

func rowMaps(rows []apiv1.Row) []map[string]string {
	out := make([]map[string]string, len(rows))
	for i, r := range rows {
		out[i] = map[string]string(r)
	}
	return out
}

func toCounts(c domain.ImportCounts) apiv1.ImportCounts {
	return apiv1.ImportCounts{Total: c.Total, Add: c.Add, Same: c.Same, Change: c.Chg, Error: c.Err, Warn: c.Warn}
}

// PreviewImport 는 JSON 행을 양식으로 바꿔 화면과 같은 미리보기를 한다. 아무것도 반영하지 않는다.
func (s *Service) PreviewImport(ctx context.Context, c apiv1.Caller, target string, rows []apiv1.Row) (apiv1.ImportPreview, apiv1.WriteMeta, error) {
	const action = "api.import_preview"
	flowTarget, err := s.importTarget(target)
	if err != nil {
		return apiv1.ImportPreview{}, apiv1.WriteMeta{}, err
	}
	if err := s.throttle(ctx, c); err != nil {
		return apiv1.ImportPreview{}, apiv1.WriteMeta{}, err
	}
	data, err := importx.BuildXLSXFromRows(flowTarget, rowMaps(rows))
	var pv domain.ImportPreview
	if err == nil {
		pv, err = s.o.Import.Preview(ctx, flowTarget, "", data)
	}
	if err != nil {
		s.failed(ctx, c.Principal, action, audit.TargetImport, "-", target, err)
		return apiv1.ImportPreview{}, apiv1.WriteMeta{}, err
	}
	out := apiv1.ImportPreview{BatchNo: pv.BatchNo, ApplyToken: pv.Token, Target: target, Counts: toCounts(pv.Counts), Applicable: pv.Applicable, Message: pv.Message,
		Rows: make([]apiv1.ImportRowResult, 0, len(pv.Rows)), RowsTruncated: pv.RowsTruncated}
	for _, r := range pv.Rows {
		row := apiv1.ImportRowResult{RowNo: r.RowNo, Key: r.Key, Verdict: r.Verdict, Reason: r.Reason, Warn: r.Warn}
		for _, ch := range r.Changes {
			row.Changes = append(row.Changes, apiv1.ImportChange{Column: ch.Column, Old: ch.Old, New: ch.New})
		}
		out.Rows = append(out.Rows, row)
	}
	s.rec(ctx, c.Principal, action, audit.TargetImport, orDash(pv.BatchNo), audit.ResultSuccess,
		[]audit.DiffItem{{Key: "target", New: target}, {Key: "rows", New: strconv.Itoa(pv.Counts.Total)}, {Key: "batch_no", New: pv.BatchNo}}, "연동 API 가져오기 미리보기")
	return out, apiv1.WriteMeta{Status: 200}, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ApplyImport 는 미리보기와 같은 행·반영 토큰으로 반영한다(공통 흐름이 같은 판정인지 다시 확인하고 한 트랜잭션으로 반영한다). Idempotency-Key 재요청은 첫 응답을 되돌려 준다.
func (s *Service) ApplyImport(ctx context.Context, c apiv1.Caller, key, hash, target, batchNo string, req apiv1.ImportApplyRequest) (apiv1.ImportApplied, apiv1.WriteMeta, error) {
	const action = "api.import_apply"
	flowTarget, err := s.importTarget(target)
	if err != nil {
		return apiv1.ImportApplied{}, apiv1.WriteMeta{}, err
	}
	var replay apiv1.ImportApplied
	if meta, ok, err := s.idemReplay(ctx, c, key, hash, &replay); err != nil || ok {
		return replay, meta, err
	}
	if err := s.throttle(ctx, c); err != nil {
		return apiv1.ImportApplied{}, apiv1.WriteMeta{}, err
	}
	if strings.TrimSpace(req.ApplyToken) == "" {
		err := fmt.Errorf("%w: apply_token 이 필요합니다", contract.ErrValidation)
		s.failed(ctx, c.Principal, action, audit.TargetImport, batchNo, target, err)
		return apiv1.ImportApplied{}, apiv1.WriteMeta{}, err
	}
	data, err := importx.BuildXLSXFromRows(flowTarget, rowMaps(req.Rows))
	var b domain.ImportBatch
	if err == nil {
		b, err = s.o.Import.Apply(ctx, flowTarget, batchNo, req.ApplyToken, data)
	}
	if err != nil {
		s.failed(ctx, c.Principal, action, audit.TargetImport, batchNo, target, err)
		return apiv1.ImportApplied{}, apiv1.WriteMeta{}, err
	}
	out := apiv1.ImportApplied{BatchNo: b.No, Target: target, Status: b.Status, Counts: toCounts(b.Counts), AppliedAt: b.AppliedAt}
	s.rec(ctx, c.Principal, action, audit.TargetImport, b.No, audit.ResultSuccess,
		[]audit.DiffItem{{Key: "target", New: target}, {Key: "rows", New: strconv.Itoa(b.Counts.Total)}, {Key: "batch_no", New: b.No}}, "연동 API 가져오기 반영")
	ref, _ := json.Marshal(out)
	s.idemStore(ctx, c.TokenID, key, hash, 200, string(ref))
	return out, apiv1.WriteMeta{Status: 200}, nil
}

// ---- 전표 초안 ----

// DraftJournal 은 전표 초안을 제출한다. 같은 토큰의 같은 external_ref 는 내용이 같으면 기존 초안을 돌려주고(200) 다르면 409 다.
func (s *Service) DraftJournal(ctx context.Context, c apiv1.Caller, key, hash string, req apiv1.JournalDraftRequest) (apiv1.JournalDraft, apiv1.WriteMeta, error) {
	const action = "api.journal_draft"
	if s.o.Journals == nil {
		return apiv1.JournalDraft{}, apiv1.WriteMeta{}, contract.ErrNotFound
	}
	var replay apiv1.JournalDraft
	if meta, ok, err := s.idemReplay(ctx, c, key, hash, &replay); err != nil || ok {
		return replay, meta, err
	}
	if err := s.throttle(ctx, c); err != nil {
		return apiv1.JournalDraft{}, apiv1.WriteMeta{}, err
	}
	in := domain.JournalInput{PostingDate: req.PostingDate, TradeDate: req.TradeDate, Memo: req.Memo}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, domain.JournalLineInput{AccountCode: l.AccountCode, Side: l.Side, Amount: l.Amount, VendorID: l.VendorID, Memo: l.Memo})
	}
	j, created, err := s.o.Journals.CreateDraftFromAPI(ctx, in, req.ExternalRef)
	if err != nil {
		s.failed(ctx, c.Principal, action, audit.TargetJournal, "-", "journal", err)
		return apiv1.JournalDraft{}, apiv1.WriteMeta{}, err
	}
	out := apiv1.JournalDraft{No: j.No, Status: j.Status, PostingDate: j.PostingDate, TradeDate: j.TradeDate, TotalAmount: j.TotalAmount, ExternalRef: strings.TrimSpace(req.ExternalRef)}
	status := 201
	if !created {
		status = 200
	}
	ref, _ := json.Marshal(out)
	s.idemStore(ctx, c.TokenID, key, hash, status, string(ref))
	return out, apiv1.WriteMeta{Status: status}, nil
}
