package aicost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 월 배부 확정·해제·전표 다시 만들기(명세 8절). 확정은 한 트랜잭션에서 실행·줄·전표 초안·감사를 함께 쓴다(하나라도 실패하면 모두 취소).

const (
	runConfirmed = "confirmed"
	runReleased  = "released"

	releaseTTL = 7 * 24 * time.Hour

	msgLocked = "확정된 달은 다시 계산할 수 없습니다. 확정 해제를 요청하세요."
)

// RunLine 은 확정된 부서별 결과다.
type RunLine struct {
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	Headcount      int64  `json:"headcount"`
	Calls          int64  `json:"calls"`
	DirectKRW      int64  `json:"directKrw"`
	SharedKRW      int64  `json:"sharedKrw"`
	TotalKRW       int64  `json:"totalKrw"`
}

// ReleaseInfo 는 해제 요청 한 건이다.
type ReleaseInfo struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"` // pending|confirmed|rejected|canceled|expired(7일 지남)
	Reason          string `json:"reason"`
	RequestedBy     int64  `json:"requestedBy"`
	RequestedByName string `json:"requestedByName"`
	RequestedAt     string `json:"requestedAt"`
}

// RunInfo 는 월·법인의 배부 실행이다.
type RunInfo struct {
	ID              int64        `json:"id"`
	Month           string       `json:"month"`
	EntityID        int64        `json:"entityId"`
	RuleVersion     int          `json:"ruleVersion"`
	Status          string       `json:"status"`
	FxUsdKrwX1000   int64        `json:"fxUsdKrwX1000"`
	TotalKRW        int64        `json:"totalKrw"`
	DirectKRW       int64        `json:"directKrw"`
	SharedKRW       int64        `json:"sharedKrw"`
	JournalNo       string       `json:"journalNo"`
	JournalStatus   string       `json:"journalStatus"`
	ConfirmedByName string       `json:"confirmedByName"`
	ConfirmedAt     string       `json:"confirmedAt"`
	ReleasedAt      string       `json:"releasedAt,omitempty"`
	Lines           []RunLine    `json:"lines"`
	Pools           []PoolResult `json:"pools"`
	Release         *ReleaseInfo `json:"release,omitempty"`
	// 법인 간 공용 풀의 몫과 상대(지급) 법인: 표시만 한다(상계·연결 조정 없음).
	GroupShareKRW        int64  `json:"groupShareKrw"`
	CounterpartyEntityID int64  `json:"counterpartyEntityId,omitempty"`
	CounterpartyName     string `json:"counterpartyName,omitempty"`
}

type runDetail struct {
	Pools                []PoolResult     `json:"pools"`
	Adjusted             []ProviderAdjust `json:"adjusted"`
	GroupShareKRW        int64            `json:"groupShareKrw,omitempty"`
	CounterpartyEntityID int64            `json:"counterpartyEntityId,omitempty"`
}

// ConfirmInput 은 확정 요청이다. EntityID 0 은 현재 법인, Version 0 은 그 달에 적용되는 규칙 버전이다.
type ConfirmInput struct {
	Month    string
	EntityID int64
	Version  int
}

func lastDayOf(month string) string { return lastDay(month) }

func (s *Service) today() string { return s.now().In(contract.KST).Format("2006-01-02") }

// monthConfirmed 는 월에 확정된 실행이 하나라도 있는지다(환율·청구액 잠금).
func (s *Service) monthConfirmed(ctx context.Context, month string) (bool, error) {
	var n int
	err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_runs WHERE month = ? AND status = 'confirmed'`, month).Scan(&n)
	return n > 0, err
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate key")
}

func (s *Service) expenseAccount(ctx context.Context) string {
	var v string
	_ = s.o.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'aicost.expense_account'`).Scan(&v)
	if v = strings.TrimSpace(v); v == "" {
		return "5290"
	}
	return v
}

// sodCheck 는 권한 분리 판정이다. 판정기가 없으면 통과한다.
func (s *Service) sodCheck(ctx context.Context, tx contract.Tx, p contract.Principal, rule string, other int64, ref sod.Ref) error {
	if s.o.SoD == nil || other == 0 {
		return nil
	}
	_, err := s.o.SoD.Evaluate(ctx, tx, sod.Check{Rule: rule, Actor: p, Subject: ref, Other: other})
	return err
}

func (s *Service) noteBlocked(ctx context.Context, err error) {
	var be *sod.BlockedError
	if s.o.SoD != nil && errors.As(err, &be) {
		s.o.SoD.RecordBlocked(ctx, be.Check)
	}
}

// ConfirmRun 은 월·법인의 배부를 확정한다(cost:allocate + ledger:write). 확정은 비용 전표 초안을 함께 만든다.
func (s *Service) ConfirmRun(ctx context.Context, in ConfirmInput) (RunInfo, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	if !p.Can(contract.PermLedgerWrite) {
		return RunInfo{}, fmt.Errorf("%w: 비용 전표 초안을 만들 권한(전표 입력)이 필요합니다", contract.ErrForbidden)
	}
	if s.o.Journals == nil {
		return RunInfo{}, fmt.Errorf("%w: 회계 서비스가 연결되지 않아 확정할 수 없습니다", contract.ErrInvalidState)
	}
	entity, err := s.scopedEntity(ctx, in.EntityID)
	if err != nil {
		return RunInfo{}, err
	}
	month, err := s.normMonth(in.Month)
	if err != nil {
		return RunInfo{}, err
	}
	if month >= s.currentMonth() {
		return RunInfo{}, fmt.Errorf("%w: 아직 끝나지 않은 달은 확정할 수 없습니다", contract.ErrInvalidState)
	}
	if done, err := s.confirmedFor(ctx, month, entity); err != nil {
		return RunInfo{}, err
	} else if done {
		return RunInfo{}, fmt.Errorf("%w: 이미 확정된 달입니다. 다시 계산하려면 확정 해제를 요청하세요.", contract.ErrInvalidState)
	}
	pv, _, err := s.computePreview(ctx, month, entity, in.Version)
	if err != nil {
		return RunInfo{}, err
	}
	if len(pv.Blockers) > 0 {
		return RunInfo{}, fmt.Errorf("%w: %s", contract.ErrInvalidState, pv.Blockers[0])
	}
	rs, err := s.ruleSetFor(ctx, month, pv.RuleVersion)
	if err != nil {
		return RunInfo{}, err
	}
	detail, _ := json.Marshal(runDetail{Pools: pv.Pools, Adjusted: pv.Adjusted, GroupShareKRW: pv.GroupShareKRW, CounterpartyEntityID: pv.CounterpartyEntityID})
	ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: entity})
	expCode := s.expenseAccount(ctx) // 트랜잭션 밖에서 읽는다(연결 하나)
	var runID int64
	var journalNo string
	err = s.o.Txs.WithTx(ectx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_runs WHERE month = ? AND entity_id = ? AND status = 'confirmed'`, month, entity).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: 이미 확정된 달입니다. 다시 계산하려면 확정 해제를 요청하세요.", contract.ErrInvalidState)
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO ai_cost_runs (month, entity_id, rule_version, status, fx_usd_krw_x1000, total_krw, direct_krw, shared_krw, journal_no, confirmed_by, confirmed_at, detail_json)
			VALUES (?,?,?, 'confirmed', ?,?,?,?, '', ?,?,?) RETURNING id`, month, entity, pv.RuleVersion, pv.FxUsdKrwX1000, pv.TotalKRW, pv.DirectKRW, pv.SharedKRW, p.UserID, s.stamp(), string(detail)).Scan(&runID); err != nil {
			return err
		}
		// 권한 분리 판정은 실행 번호가 생긴 뒤(같은 트랜잭션)에 한다: 위반 기록·감사의 대상 번호가 이 실행이 된다. 차단이면 이 트랜잭션이 통째로 취소된다.
		if err := s.sodCheck(ctx, tx, p, sod.RuleAICostRuleConfirm, rs.CreatedBy, sod.Ref{Type: "ai_cost_run", ID: runID, No: month}); err != nil {
			return err
		}
		var jl []domain.AICostJournalLine
		for _, l := range pv.Lines {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_run_lines (run_id, department_id, department_name, headcount, calls, direct_krw, shared_krw, total_krw) VALUES (?,?,?,?,?,?,?,?)`,
				runID, l.DepartmentID, l.DepartmentName, l.Headcount, l.Calls, l.DirectKRW, l.SharedKRW, l.TotalKRW); err != nil {
				return err
			}
			if l.TotalKRW > 0 {
				jl = append(jl, domain.AICostJournalLine{DepartmentID: l.DepartmentID, Amount: l.TotalKRW, Memo: fmt.Sprintf("%s AI 비용(직접 %s원 + 공용 %s원)", l.DepartmentName, thousandsKRW(l.DirectKRW), thousandsKRW(l.SharedKRW))})
			}
		}
		if len(jl) > 0 {
			no, err := s.o.Journals.CreateAICostDraft(ectx, tx, domain.AICostJournalInput{RunID: runID, PostingDate: lastDayOf(month), TradeDate: s.today(),
				Memo: runMemo(month, pv.RuleVersion, pv.GroupShareKRW, pv.CounterpartyEntityID), ExpenseCode: expCode, Lines: jl})
			if err != nil {
				return err
			}
			journalNo = no
			if _, err := tx.ExecContext(ctx, `UPDATE ai_cost_runs SET journal_no = ? WHERE id = ?`, no, runID); err != nil {
				return err
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.run_confirm", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("run/%s/%d", month, entity), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: month}, {Key: "version", New: strconv.Itoa(pv.RuleVersion)}, {Key: "amount", New: strconv.FormatInt(pv.TotalKRW, 10)}, {Key: "journal_no", New: journalNo}}})
	})
	if err != nil {
		s.noteBlocked(ctx, err)
		if isUniqueViolation(err) {
			return RunInfo{}, fmt.Errorf("%w: 이미 확정된 달입니다. 다시 계산하려면 확정 해제를 요청하세요.", contract.ErrInvalidState)
		}
		return RunInfo{}, err
	}
	return s.runByID(ctx, runID)
}

func (s *Service) confirmedFor(ctx context.Context, month string, entity int64) (bool, error) {
	var n int
	err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_cost_runs WHERE month = ? AND entity_id = ? AND status = 'confirmed'`, month, entity).Scan(&n)
	return n > 0, err
}

// GetRun 은 월·법인의 실행(확정 우선, 없으면 가장 최근 해제분)이다(cost:read). 없으면 nil.
func (s *Service) GetRun(ctx context.Context, month string, entityID int64) (*RunInfo, error) {
	entity, err := s.scopedEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	month, err = s.normMonth(month)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.o.DB.QueryRowContext(ctx, `SELECT id FROM ai_cost_runs WHERE month = ? AND entity_id = ? ORDER BY CASE status WHEN 'confirmed' THEN 0 ELSE 1 END, id DESC LIMIT 1`, month, entity).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := s.runByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) runByID(ctx context.Context, id int64) (RunInfo, error) {
	var r RunInfo
	var releasedAt sql.NullString
	var detail string
	err := s.o.DB.QueryRowContext(ctx, `SELECT r.id, r.month, r.entity_id, r.rule_version, r.status, r.fx_usd_krw_x1000, r.total_krw, r.direct_krw, r.shared_krw, r.journal_no, COALESCE(u.name, ''), r.confirmed_at, r.released_at, r.detail_json
		FROM ai_cost_runs r LEFT JOIN users u ON u.id = r.confirmed_by WHERE r.id = ?`, id).
		Scan(&r.ID, &r.Month, &r.EntityID, &r.RuleVersion, &r.Status, &r.FxUsdKrwX1000, &r.TotalKRW, &r.DirectKRW, &r.SharedKRW, &r.JournalNo, &r.ConfirmedByName, &r.ConfirmedAt, &releasedAt, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return RunInfo{}, fmt.Errorf("%w: 배부 실행을 찾을 수 없습니다", contract.ErrNotFound)
	}
	if err != nil {
		return RunInfo{}, err
	}
	r.ReleasedAt = releasedAt.String
	var d runDetail
	if detail != "" {
		_ = json.Unmarshal([]byte(detail), &d)
	}
	r.Pools = d.Pools
	r.GroupShareKRW, r.CounterpartyEntityID = d.GroupShareKRW, d.CounterpartyEntityID
	if r.CounterpartyEntityID > 0 {
		_ = s.o.DB.QueryRowContext(ctx, `SELECT name FROM entities WHERE id = ?`, r.CounterpartyEntityID).Scan(&r.CounterpartyName)
	}
	if r.Pools == nil {
		r.Pools = []PoolResult{}
	}
	r.Lines = []RunLine{}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT department_id, department_name, headcount, calls, direct_krw, shared_krw, total_krw FROM ai_cost_run_lines WHERE run_id = ? ORDER BY department_id`, id)
	if err != nil {
		return RunInfo{}, err
	}
	for rows.Next() {
		var l RunLine
		if err := rows.Scan(&l.DepartmentID, &l.DepartmentName, &l.Headcount, &l.Calls, &l.DirectKRW, &l.SharedKRW, &l.TotalKRW); err != nil {
			rows.Close()
			return RunInfo{}, err
		}
		r.Lines = append(r.Lines, l)
	}
	if err := rows.Close(); err != nil {
		return RunInfo{}, err
	}
	if r.JournalNo != "" && s.o.Journals != nil {
		ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: r.EntityID})
		if st, err := s.o.Journals.AICostJournalStatus(ectx, s.o.DB, r.JournalNo); err == nil {
			r.JournalStatus = st
		}
	}
	var rel ReleaseInfo
	var at string
	err = s.o.DB.QueryRowContext(ctx, `SELECT q.id, q.status, q.reason, q.requested_by, COALESCE(u.name, ''), q.requested_at FROM ai_cost_release_requests q LEFT JOIN users u ON u.id = q.requested_by
		WHERE q.run_id = ? ORDER BY q.id DESC LIMIT 1`, id).Scan(&rel.ID, &rel.Status, &rel.Reason, &rel.RequestedBy, &rel.RequestedByName, &at)
	if err == nil {
		rel.RequestedAt = at
		if rel.Status == "pending" && s.releaseStale(at) {
			rel.Status = "expired"
		}
		r.Release = &rel
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RunInfo{}, err
	}
	return r, nil
}

func (s *Service) releaseStale(requestedAt string) bool {
	t, err := time.Parse("2006-01-02T15:04:05Z", requestedAt)
	return err == nil && s.now().Sub(t) > releaseTTL
}

type pendingRelease struct {
	id          int64
	requestedBy int64
	requestedAt string
}

func (s *Service) loadRunForRelease(ctx context.Context, runID int64) (RunInfo, error) {
	r, err := s.runByID(ctx, runID)
	if err != nil {
		return RunInfo{}, err
	}
	scope, err := contract.ReadScope(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	if !scope.Has(r.EntityID) {
		return RunInfo{}, fmt.Errorf("%w: 그 법인의 배부 실행은 다룰 수 없습니다", contract.ErrForbidden)
	}
	if r.Status != runConfirmed {
		return RunInfo{}, fmt.Errorf("%w: 확정된 실행이 아닙니다", contract.ErrInvalidState)
	}
	return r, nil
}

func journalBlock(status string) error {
	switch status {
	case domain.JournalApproved:
		return fmt.Errorf("%w: 승인된 전표가 연결되어 있어 해제할 수 없습니다. 먼저 전표를 역분개하세요.", contract.ErrInvalidState)
	case domain.JournalRequested:
		return fmt.Errorf("%w: 전표가 승인 요청 중입니다. 먼저 전표의 승인 요청을 회수하세요.", contract.ErrInvalidState)
	}
	return nil
}

// RequestRelease 는 확정 해제를 요청한다(cost:allocate). 다른 재무 담당이 확인해야 해제된다.
func (s *Service) RequestRelease(ctx context.Context, runID int64, reason string) (RunInfo, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	r, err := s.loadRunForRelease(ctx, runID)
	if err != nil {
		return RunInfo{}, err
	}
	reason = strings.TrimSpace(reason)
	if n := len([]rune(reason)); n < 1 || n > 200 {
		return RunInfo{}, valErr("해제 사유를 1~200자로 입력하세요.")
	}
	if err := journalBlock(r.JournalStatus); err != nil {
		return RunInfo{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var pend pendingRelease
		err := tx.QueryRowContext(ctx, `SELECT id, requested_by, requested_at FROM ai_cost_release_requests WHERE run_id = ? AND status = 'pending'`, runID).Scan(&pend.id, &pend.requestedBy, &pend.requestedAt)
		switch {
		case err == nil && !s.releaseStale(pend.requestedAt):
			return fmt.Errorf("%w: 이미 해제 요청이 있습니다. 다른 재무 담당의 확인을 기다리는 중입니다.", contract.ErrInvalidState)
		case err == nil: // 7일이 지난 요청은 닫고 새로 받는다
			if _, e := tx.ExecContext(ctx, `UPDATE ai_cost_release_requests SET status = 'canceled', resolved_at = ? WHERE id = ?`, s.stamp(), pend.id); e != nil {
				return e
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_cost_release_requests (run_id, requested_by, requested_at, reason, status) VALUES (?,?,?,?, 'pending')`, runID, p.UserID, s.stamp(), reason); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.run_release_request", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("run/%s/%d", r.Month, r.EntityID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: r.Month}, {Key: "reason", New: fmt.Sprintf("%d자", len([]rune(reason)))}}})
	})
	if err != nil {
		return RunInfo{}, err
	}
	return s.runByID(ctx, runID)
}

// ConfirmRelease 는 다른 사람의 해제 요청을 확인해 확정을 풀고 임시 전표를 취소한다(cost:allocate, 요청자와 달라야 한다).
func (s *Service) ConfirmRelease(ctx context.Context, runID int64) (RunInfo, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	r, err := s.loadRunForRelease(ctx, runID)
	if err != nil {
		return RunInfo{}, err
	}
	if s.o.Journals == nil {
		return RunInfo{}, fmt.Errorf("%w: 회계 서비스가 연결되지 않았습니다", contract.ErrInvalidState)
	}
	ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: r.EntityID})
	err = s.o.Txs.WithTx(ectx, func(tx contract.Tx) error {
		var pend pendingRelease
		err := tx.QueryRowContext(ctx, `SELECT id, requested_by, requested_at FROM ai_cost_release_requests WHERE run_id = ? AND status = 'pending'`, runID).Scan(&pend.id, &pend.requestedBy, &pend.requestedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: 확인할 해제 요청이 없습니다", contract.ErrInvalidState)
		}
		if err != nil {
			return err
		}
		if pend.requestedBy == p.UserID {
			return fmt.Errorf("%w: 해제는 요청한 사람이 확인할 수 없습니다.", contract.ErrInvalidState)
		}
		if s.releaseStale(pend.requestedAt) {
			if _, e := tx.ExecContext(ctx, `UPDATE ai_cost_release_requests SET status = 'canceled', resolved_at = ? WHERE id = ?`, s.stamp(), pend.id); e != nil {
				return e
			}
			return errExpired
		}
		if r.JournalNo != "" {
			st, err := s.o.Journals.AICostJournalStatus(ectx, tx, r.JournalNo)
			if err != nil {
				return err
			}
			if err := journalBlock(st); err != nil {
				return err
			}
			if err := s.o.Journals.CancelAICostDraft(ectx, tx, r.JournalNo, fmt.Sprintf("%s AI 비용 배부 확정 해제", r.Month)); err != nil {
				return err
			}
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, `UPDATE ai_cost_release_requests SET status = 'confirmed', confirmed_by = ?, resolved_at = ? WHERE id = ?`, p.UserID, now, pend.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_cost_runs SET status = 'released', released_by = ?, released_at = ? WHERE id = ?`, p.UserID, now, runID); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.run_release", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("run/%s/%d", r.Month, r.EntityID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: r.Month}, {Key: "journal_no", Old: r.JournalNo}}})
	})
	if errors.Is(err, errExpired) {
		return RunInfo{}, fmt.Errorf("%w: 해제 요청이 7일이 지나 만료되었습니다. 다시 요청하세요.", contract.ErrInvalidState)
	}
	if err != nil {
		return RunInfo{}, err
	}
	return s.runByID(ctx, runID)
}

var errExpired = errors.New("해제 요청 만료")

// RejectRelease 는 해제 요청을 반려한다(요청자 본인이 하면 취소). 확정은 그대로다.
func (s *Service) RejectRelease(ctx context.Context, runID int64) (RunInfo, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	r, err := s.loadRunForRelease(ctx, runID)
	if err != nil {
		return RunInfo{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var pend pendingRelease
		err := tx.QueryRowContext(ctx, `SELECT id, requested_by, requested_at FROM ai_cost_release_requests WHERE run_id = ? AND status = 'pending'`, runID).Scan(&pend.id, &pend.requestedBy, &pend.requestedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: 반려할 해제 요청이 없습니다", contract.ErrInvalidState)
		}
		if err != nil {
			return err
		}
		status := "rejected"
		if pend.requestedBy == p.UserID {
			status = "canceled"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_cost_release_requests SET status = ?, resolved_at = ? WHERE id = ?`, status, s.stamp(), pend.id); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.run_release_reject", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("run/%s/%d", r.Month, r.EntityID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: r.Month}}})
	})
	if err != nil {
		return RunInfo{}, err
	}
	return s.runByID(ctx, runID)
}

// RegenerateJournal 은 전표가 취소된 확정 실행의 비용 전표 초안을 같은 줄로 다시 만든다(cost:allocate + ledger:write).
func (s *Service) RegenerateJournal(ctx context.Context, runID int64) (RunInfo, error) {
	p, err := contract.Authorize(ctx, contract.PermCostAllocate)
	if err != nil {
		return RunInfo{}, err
	}
	if !p.Can(contract.PermLedgerWrite) {
		return RunInfo{}, fmt.Errorf("%w: 비용 전표 초안을 만들 권한(전표 입력)이 필요합니다", contract.ErrForbidden)
	}
	r, err := s.loadRunForRelease(ctx, runID)
	if err != nil {
		return RunInfo{}, err
	}
	if s.o.Journals == nil {
		return RunInfo{}, fmt.Errorf("%w: 회계 서비스가 연결되지 않았습니다", contract.ErrInvalidState)
	}
	if r.JournalNo != "" && r.JournalStatus != domain.JournalCanceled {
		return RunInfo{}, fmt.Errorf("%w: 이미 전표가 있습니다.", contract.ErrInvalidState)
	}
	var jl []domain.AICostJournalLine
	for _, l := range r.Lines {
		if l.TotalKRW > 0 {
			jl = append(jl, domain.AICostJournalLine{DepartmentID: l.DepartmentID, Amount: l.TotalKRW, Memo: fmt.Sprintf("%s AI 비용(직접 %s원 + 공용 %s원)", l.DepartmentName, thousandsKRW(l.DirectKRW), thousandsKRW(l.SharedKRW))})
		}
	}
	if len(jl) == 0 {
		return RunInfo{}, fmt.Errorf("%w: 전표로 만들 금액이 없습니다", contract.ErrInvalidState)
	}
	ectx := contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: r.EntityID})
	expCode := s.expenseAccount(ctx)
	err = s.o.Txs.WithTx(ectx, func(tx contract.Tx) error {
		no, err := s.o.Journals.CreateAICostDraft(ectx, tx, domain.AICostJournalInput{RunID: runID, PostingDate: lastDayOf(r.Month), TradeDate: s.today(),
			Memo: runMemo(r.Month, r.RuleVersion, r.GroupShareKRW, r.CounterpartyEntityID), ExpenseCode: expCode, Lines: jl})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_cost_runs SET journal_no = ? WHERE id = ?`, no, runID); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.journal_regenerate", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("run/%s/%d", r.Month, r.EntityID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: r.Month}, {Key: "journal_no", Old: r.JournalNo, New: no}}})
	})
	if err != nil {
		return RunInfo{}, err
	}
	return s.runByID(ctx, runID)
}

// runMemo 는 비용 전표의 메모다. 법인 간 공용 비용이 들어 있으면 지급 법인을 적는다(표시만, 상계 없음).
func runMemo(month string, version int, groupShare, counterparty int64) string {
	m := fmt.Sprintf("%s AI 사용 비용 배부(규칙 v%d)", month, version)
	if groupShare > 0 && counterparty > 0 {
		m += fmt.Sprintf(", 법인 간 공용 비용 %s원 포함(지급 법인 %d)", thousandsKRW(groupShare), counterparty)
	}
	return m
}
