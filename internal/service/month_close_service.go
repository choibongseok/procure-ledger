package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 월 마감(B27 ②, 3.2·4절).
// 월 상태 전이는 monthTransitions 표 한 곳에서만 정의한다(표에 없는 전이는 전부 거부). 마감할 수 있는 달 판정(closeReason)과 마감 전 점검(precheck)도 한 곳이다.

// 월 상태 전이 표.
type monthEvent string

const (
	mevClose          monthEvent = "close"           // 마감
	mevRequestRelease monthEvent = "request_release" // 해제 요청 상신(결재 중)
	mevReleaseOK      monthEvent = "release_ok"      // 해제 최종 승인
	mevReleaseNo      monthEvent = "release_no"      // 해제 반려·회수·상신 취소
)

// 상태 값: "" = 기록 없음(열림), open·closed·release_pending.
var monthTransitions = map[string]map[monthEvent]string{
	"":                         {mevClose: domain.MonthClosed},
	domain.MonthOpen:           {mevClose: domain.MonthClosed},
	domain.MonthClosed:         {mevRequestRelease: domain.MonthReleasePending},
	domain.MonthReleasePending: {mevReleaseOK: domain.MonthOpen, mevReleaseNo: domain.MonthClosed},
}

func monthTransition(from string, ev monthEvent) (string, error) {
	if to, ok := monthTransitions[from][ev]; ok {
		return to, nil
	}
	return "", fmt.Errorf("%w: 현재 상태에서는 할 수 없는 월 마감 동작입니다", contract.ErrInvalidState)
}

// MonthCloseService 는 domain.MonthCloseService 의 구현이다.
type MonthCloseService struct {
	Base
	Repo repository.SQLMonthClose
}

var _ domain.MonthCloseService = (*MonthCloseService)(nil)

func NewMonthCloseService(b Base) *MonthCloseService { return &MonthCloseService{Base: b} }

// ---- 월 계산 ----

func parseMonthText(m string) (time.Time, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(m))
	if err != nil || len(strings.TrimSpace(m)) != 7 {
		return time.Time{}, validation("월은 YYYY-MM 형식으로 입력하세요(예 2026-11)")
	}
	return t, nil
}

func monthRange(t time.Time) (start, end string) {
	return t.Format("2006-01-02"), t.AddDate(0, 1, -1).Format("2006-01-02")
}

func monthLabel(m string) string { return monthKorean(m) }

func addMonths(m string, n int) string {
	t, _ := time.Parse("2006-01", m)
	return t.AddDate(0, n, 0).Format("2006-01")
}

// closeReason 은 그 달을 지금 마감할 수 없는 이유다(없으면 ""). conflict 가 true 면 이미 마감된 달(409)이다.
func (s *MonthCloseService) closeReason(ctx context.Context, q contract.Tx, month string, all []repository.MonthCloseRec) (reason string, conflict bool, err error) {
	closed := map[string]bool{}
	for _, r := range all {
		if r.Status == "closed" {
			closed[r.Month] = true
		}
	}
	if closed[month] {
		return "이미 마감된 달입니다.", true, nil
	}
	cur := budgetMonthOf(s.now())
	if month >= cur {
		return "이번 달이 끝난 뒤에 마감할 수 있습니다.", false, nil
	}
	first, err := s.Repo.EarliestLedgerMonth(ctx, q)
	if err != nil {
		return "", false, err
	}
	if first == "" {
		return "마감할 재고 기록이 없습니다.", false, nil
	}
	if month < first {
		return fmt.Sprintf("이 달에는 재고 기록이 없습니다. 첫 기록이 있는 달(%s)부터 마감하세요.", monthLabel(first)), false, nil
	}
	for m := first; m < month; m = addMonths(m, 1) {
		if !closed[m] {
			return fmt.Sprintf("앞선 달(%s)이 아직 마감되지 않았습니다. 순서대로 마감하세요.", monthLabel(m)), false, nil
		}
	}
	return "", false, nil
}

// precheck 는 마감 전 점검(차단·경고)과 마감 가능 여부다. 읽기 전용이다.
func (s *MonthCloseService) precheck(ctx context.Context, q contract.Tx, month string) (domain.Precheck, error) {
	t, err := parseMonthText(month)
	if err != nil {
		return domain.Precheck{}, err
	}
	month = t.Format("2006-01")
	_, end := monthRange(t)
	out := domain.Precheck{Month: month, Blockers: []domain.PrecheckItem{}, Warnings: []domain.PrecheckItem{}}
	// B31: 전기일이 그 달이고 승인 요청 중인 전표는 차단, 임시 전표는 경고다(전표는 재고와 같은 마감을 쓴다).
	jr, err := repository.SQLLedger{}.RequestedJournals(ctx, q, month)
	if err != nil {
		return out, err
	}
	for _, d := range jr {
		out.Blockers = append(out.Blockers, domain.PrecheckItem{Kind: domain.PrecheckBlocker, Title: "승인 요청 중인 전표", DocID: d.DocID,
			Detail: fmt.Sprintf("전표 %s(%s)가 승인 요청 중입니다. 승인을 끝내거나 회수한 뒤 다시 점검하세요.", d.CountNo, d.DocNo)})
	}
	if dj, err := (repository.SQLLedger{}).DraftJournals(ctx, q, month); err != nil {
		return out, err
	} else if len(dj) > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "임시 전표",
			Detail: fmt.Sprintf("%s 전기일의 임시 전표가 %d건 있습니다. 마감하면 이 전표로는 입력·승인할 수 없습니다.", monthLabel(month), len(dj))})
	}
	if rp, err := (repository.SQLPayments{}).RequestedPayments(ctx, q, month); err != nil {
		return out, err
	} else {
		for _, d := range rp {
			out.Blockers = append(out.Blockers, domain.PrecheckItem{Kind: domain.PrecheckBlocker, Title: "승인 요청 중인 지급 요청서", DocID: d.DocID,
				Detail: fmt.Sprintf("지급 요청 %s(%s)가 승인 요청 중입니다. 승인을 끝내거나 회수한 뒤 다시 점검하세요.", d.CountNo, d.DocNo)})
		}
	}
	if ap, err := (repository.SQLPayments{}).ApprovedUnpaid(ctx, q, month); err != nil {
		return out, err
	} else if len(ap) > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "지급 처리 전 승인 건",
			Detail: fmt.Sprintf("%s 지급 예정인 승인된 지급 건이 %d건 아직 지급 처리되지 않았습니다.", monthLabel(month), len(ap))})
	}
	if n, err := (repository.SQLCards{}).UnreconciledCount(ctx, q, month); err != nil {
		return out, err
	} else if n > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "소명하지 않은 카드 내역",
			Detail: fmt.Sprintf("%s에 사용한 카드 내역 %d건이 아직 소명되지 않았습니다. 소지자에게 소명을 안내하세요.", monthLabel(month), n)})
	}
	if n, err := (repository.SQLLedger{}).PendingQueueCount(ctx, q, month); err != nil {
		return out, err
	} else if n > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "처리하지 않은 전표 대기",
			Detail: fmt.Sprintf("%s에 올라온 전표 대기가 %d건 처리되지 않았습니다. 전표 대기 화면에서 전표를 만들거나 불필요로 처리하세요.", monthLabel(month), n)})
	}
	adj, err := s.Repo.ApprovingAdjustments(ctx, q, month)
	if err != nil {
		return out, err
	}
	for _, d := range adj {
		out.Blockers = append(out.Blockers, domain.PrecheckItem{Kind: domain.PrecheckBlocker, Title: "결재 진행 중인 재고 조정 전표", DocID: d.DocID,
			Detail: fmt.Sprintf("%s(실사 %s)가 결재 중입니다. 결재를 끝내거나 회수한 뒤 다시 점검하세요.", d.DocNo, d.CountNo)})
	}
	if late, err := s.Repo.LatePOs(ctx, q, end); err != nil {
		return out, err
	} else if len(late) > 0 {
		shown := late
		if len(shown) > 5 {
			shown = shown[:5]
		}
		more := ""
		if len(late) > len(shown) {
			more = fmt.Sprintf(" 외 %d건", len(late)-len(shown))
		}
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "입고가 끝나지 않은 발주",
			Detail: fmt.Sprintf("납기가 %s 말 이전인데 입고가 끝나지 않은 발주가 %d건 있습니다: %s%s", monthLabel(month), len(late), strings.Join(shown, ", "), more)})
	}
	oc, err := s.Repo.OpenCounts(ctx, q, month)
	if err != nil {
		return out, err
	}
	for _, d := range oc {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "작성 중인 실사 표",
			Detail: fmt.Sprintf("실사 %s 가 아직 상신되지 않았습니다. 마감하면 이 표로는 조정할 수 없으니 새로 실사하세요.", d.CountNo)})
	}
	if n, err := s.Repo.WarnedAdjustments(ctx, q, month); err != nil {
		return out, err
	} else if n > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "평균 단가를 알 수 없는 조정",
			Detail: fmt.Sprintf("입고 이력이 없어 0원으로 평가한 조정 기록이 %d건 있습니다. 금액이 실제와 다를 수 있습니다.", n)})
	}
	if n, err := (repository.SQLImport{}).PendingOpeningJournals(ctx, q, month); err != nil {
		return out, err
	} else if n > 0 {
		out.Warnings = append(out.Warnings, domain.PrecheckItem{Kind: domain.PrecheckWarning, Title: "승인 전 기초 전표",
			Detail: fmt.Sprintf("기초 데이터 가져오기로 만든 기초 전표 %d건이 아직 승인되지 않았습니다. 기초 재고의 수량·금액은 이미 재고에 들어갔지만 계정별 원장·시산표에는 승인 뒤에 들어갑니다.", n)})
	}
	all, err := s.Repo.All(ctx, q)
	if err != nil {
		return out, err
	}
	reason, _, err := s.closeReason(ctx, q, month, all)
	if err != nil {
		return out, err
	}
	if reason == "" && len(out.Blockers) > 0 {
		switch {
		case len(jr) == 0:
			reason = fmt.Sprintf("결재 진행 중인 재고 조정 전표 %d건이 있어 마감할 수 없습니다. 결재를 끝내거나 회수한 뒤 다시 점검하세요.", len(out.Blockers))
		case len(adj) == 0:
			reason = fmt.Sprintf("승인 요청 중인 전표 %d건이 있어 마감할 수 없습니다. 승인을 끝내거나 회수한 뒤 다시 점검하세요.", len(out.Blockers))
		default:
			reason = fmt.Sprintf("결재 진행 중인 전표·재고 조정 전표 %d건이 있어 마감할 수 없습니다. 결재를 끝내거나 회수한 뒤 다시 점검하세요.", len(out.Blockers))
		}
	}
	out.Reason, out.Closable = reason, reason == ""
	return out, nil
}

// ---- 서비스 ----

func (s *MonthCloseService) valuationAllowed(p contract.Principal) bool {
	return p.Can(contract.PermInventoryValuation)
}

// rowFor 는 월 마감 행을 화면용 줄로 바꾼다.
func (s *MonthCloseService) rowFor(ctx context.Context, q contract.Tx, p contract.Principal, month string, rec repository.MonthCloseRec, have bool) (domain.MonthRow, error) {
	row := domain.MonthRow{Month: month, State: domain.MonthOpen}
	if !have {
		return row, nil
	}
	row.Version = rec.Version
	if rec.Status == "closed" {
		row.State, row.ClosedAt, row.ClosedBy = domain.MonthClosed, rec.ClosedAt, rec.ClosedName
		if doc, err := s.Repo.PendingRelease(ctx, q, month); err != nil {
			return row, err
		} else if doc != 0 {
			row.State = domain.MonthReleasePending // 해제 요청서가 결재 중이면 마감 그대로 두고 상태만 그렇게 보인다(문서 상태에서 파생)
		}
		if s.valuationAllowed(p) {
			a, err := s.Repo.CloseAmount(ctx, q, month, rec.Version)
			if err != nil {
				return row, err
			}
			row.CloseAmount = &a
		}
	}
	return row, nil
}

// List 는 첫 재고 기록이 있는 달부터 지난 달까지의 마감 현황이다(최근 달이 먼저, 최대 36개월).
func (s *MonthCloseService) List(ctx context.Context) ([]domain.MonthRow, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return nil, err
	}
	first, err := s.Repo.EarliestLedgerMonth(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	all, err := s.Repo.All(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	byMonth := map[string]repository.MonthCloseRec{}
	for _, r := range all {
		byMonth[r.Month] = r
	}
	cur := budgetMonthOf(s.now())
	out := []domain.MonthRow{}
	nextClosable, latestClosed := "", ""
	for m := first; first != "" && m < cur; m = addMonths(m, 1) {
		rec, have := byMonth[m]
		if nextClosable == "" && (!have || rec.Status != "closed") {
			nextClosable = m
		}
		if have && rec.Status == "closed" {
			latestClosed = m
		}
		row, err := s.rowFor(ctx, s.DB, p, m, rec, have)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	for i := range out {
		out[i].Closable = out[i].Month == nextClosable
		out[i].Releasable = out[i].Month == latestClosed && out[i].State == domain.MonthClosed // 가장 최근 마감 달만, 진행 중 요청이 없을 때
	}
	// 최근 달이 먼저, 최대 36개월.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > 36 {
		out = out[:36]
	}
	return out, nil
}

// Precheck 는 마감 전 점검 목록이다(읽기 전용, 감사 없음).
func (s *MonthCloseService) Precheck(ctx context.Context, month string) (domain.Precheck, error) {
	if _, err := contract.Authorize(ctx, contract.PermInventoryRead); err != nil {
		return domain.Precheck{}, err
	}
	return s.precheck(ctx, s.DB, month)
}

// Close 는 월을 마감한다. 점검 재확인 → 마감 행·스냅샷·사건 → 감사가 한 트랜잭션이고, 하나라도 실패하면 전체 취소다.
func (s *MonthCloseService) Close(ctx context.Context, month string) (domain.MonthRow, error) {
	p, err := s.authorize(ctx, contract.PermInventoryClose, "month_close.close", audit.TargetMonthClose)
	if err != nil {
		return domain.MonthRow{}, err
	}
	t, err := parseMonthText(month)
	if err != nil {
		return domain.MonthRow{}, err
	}
	month = t.Format("2006-01")
	var out domain.MonthRow
	err = s.write(ctx, p, "month_close.close", audit.TargetMonthClose, 0, func(tx contract.Tx) error {
		// 그 달에 쓰는 중인 트랜잭션(전표·입출고·실사)이 끝나길 기다린 뒤 검사·스냅샷을 만든다. 이후의 쓰기는 마감 결과를 보고 거절된다(PG, S3).
		if err := dbutil.AcquireMonthClose(ctx, tx, month); err != nil {
			return err
		}
		all, err := s.Repo.All(ctx, tx)
		if err != nil {
			return err
		}
		cur := ""
		for _, r := range all {
			if r.Month == month {
				cur = r.Status
			}
		}
		if _, err := monthTransition(cur, mevClose); err != nil {
			return fmt.Errorf("%w: 이미 마감된 달입니다.", contract.ErrInvalidState)
		}
		pc, err := s.precheck(ctx, tx, month)
		if err != nil {
			return err
		}
		if !pc.Closable {
			return validation("%s", pc.Reason)
		}
		start, end := monthRange(t)
		lines, err := s.Repo.BuildSnapshot(ctx, tx, start, end)
		if err != nil {
			return err
		}
		now := s.now()
		version, err := s.Repo.SaveClose(ctx, tx, month, p.UserID, now)
		if err != nil {
			return err
		}
		n, amount, err := s.Repo.InsertSnapshot(ctx, tx, month, version, lines)
		if err != nil {
			return err
		}
		if err := s.Repo.AddEvent(ctx, tx, month, "close", version, p.UserID, 0, now); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "month_close.close", TargetType: audit.TargetMonthClose, TargetID: month, Result: audit.ResultSuccess,
			Detail: "월 마감", Diff: []audit.DiffItem{
				{Key: "version", New: fmt.Sprint(version)}, {Key: "snapshot_lines", New: fmt.Sprint(n)},
				{Key: "close_amount", New: fmt.Sprint(amount)}, {Key: "warnings", New: fmt.Sprint(len(pc.Warnings))}}}); err != nil {
			return err
		}
		rec, have, err := s.Repo.Get(ctx, tx, month)
		if err != nil {
			return err
		}
		out, err = s.rowFor(ctx, tx, p, month, rec, have)
		return err
	})
	return out, err
}

// RequestRelease 는 마감 해제 요청서(시스템 양식 MCR)를 만들어 상신한다(재무). 가장 최근에 마감한 달만, 달당 진행 중인 요청은 하나만 된다.
// 결재선은 양식이 정한다(재무팀장 단독, 본인이면 B21 규칙대로 올림). 승인되면 후속 동작(releaseMonthHook)이 같은 트랜잭션에서 달을 연다.
// releaseMu 는 마감 해제 요청의 확인(이미 결재 중인지)·작성·상신·사건 기록을 한 덩어리로 직렬화한다(검증자 B2: 같은 달 동시 요청 4건이 모두 상신되던 경합).
// 문서 작성·상신이 별도 트랜잭션이라 DB 트랜잭션 하나로 묶을 수 없어 서버 프로세스 안의 잠금으로 막는다(이 서버는 한 프로세스가 DB 를 쓴다).
var releaseMu sync.Mutex

func (s *MonthCloseService) RequestRelease(ctx context.Context, month, reason string) (int64, error) {
	p, err := s.authorize(ctx, contract.PermInventoryClose, "month_close.release_request", audit.TargetMonthClose)
	if err != nil {
		return 0, err
	}
	t, err := parseMonthText(month)
	if err != nil {
		return 0, err
	}
	month = t.Format("2006-01")
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 10 {
		return 0, validation("마감 해제 사유를 10자 이상 입력하세요.")
	} else if n > 500 {
		return 0, validation("마감 해제 사유는 500자 이내로 입력하세요.")
	}
	releaseMu.Lock()
	defer releaseMu.Unlock()
	rec, have, err := s.Repo.Get(ctx, s.DB, month)
	if err != nil {
		return 0, err
	}
	if !have || rec.Status != "closed" {
		return 0, fmt.Errorf("%w: 마감된 달이 아닙니다.", contract.ErrInvalidState)
	}
	if doc, err := s.Repo.PendingRelease(ctx, s.DB, month); err != nil {
		return 0, err
	} else if doc != 0 {
		return 0, fmt.Errorf("%w: 이 달의 마감 해제 요청이 이미 결재 중입니다.", contract.ErrInvalidState)
	}
	if err := s.latestClosedOnly(ctx, s.DB, month); err != nil {
		return 0, err
	}
	lines, items, err := s.snapshotCounts(ctx, month, rec.Version)
	if err != nil {
		return 0, err
	}
	docs := NewApprovalDocService(s.Base)
	dctx := withSystemForm(ctx)
	fields := map[string]any{"month": month, "reason": reason,
		"summary": fmt.Sprintf("%s 마감(%d차) 월말 스냅샷 품목 %d개·%d줄이 해제 뒤 다시 만들어집니다. 이후 마감된 달은 없습니다.", monthLabel(month), rec.Version, items, lines)}
	doc, err := docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "MCR", Title: fmt.Sprintf("월 마감 해제 요청 %s", monthLabel(month)), Fields: fields})
	if err != nil {
		return 0, err
	}
	submitted, err := docs.Submit(dctx, doc.ID)
	if err != nil {
		return 0, err
	}
	err = s.write(ctx, p, "month_close.release_request", audit.TargetMonthClose, 0, func(tx contract.Tx) error {
		if err := s.Repo.AddEvent(ctx, tx, month, "release_request", rec.Version, p.UserID, doc.ID, s.now()); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "month_close.release_request", TargetType: audit.TargetMonthClose, TargetID: month, Result: audit.ResultSuccess,
			Detail: "마감 해제 요청", Diff: []audit.DiffItem{{Key: "doc_no", New: submitted.No}, {Key: "version", New: fmt.Sprint(rec.Version)}, {Key: "reason", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))}}})
	})
	return doc.ID, err
}

// latestClosedOnly 는 그 달이 가장 최근에 마감한 달인지 확인한다(해제는 거꾸로만, D4).
func (s *MonthCloseService) latestClosedOnly(ctx context.Context, q contract.Tx, month string) error {
	all, err := s.Repo.All(ctx, q)
	if err != nil {
		return err
	}
	latest := ""
	for _, r := range all {
		if r.Status == "closed" && r.Month > latest {
			latest = r.Month
		}
	}
	if latest != month {
		return validation("마감 해제는 가장 최근에 마감한 달(%s)부터 차례로 할 수 있습니다.", monthLabel(latest))
	}
	return nil
}

// snapshotCounts 는 그 마감 버전 스냅샷의 줄 수와 품목 수다. 금액은 요약에 넣지 않는다(요약은 참조 감사팀 등 평가 조회 권한이 없는 열람자도 읽는다).
func (s *MonthCloseService) snapshotCounts(ctx context.Context, month string, version int) (lines, items int, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT item_id) FROM month_close_lines WHERE `+contract.SQLEntity(ctx, "")+` AND month = ? AND version = ?`, month, version).Scan(&lines, &items)
	return lines, items, err
}

// releaseMonthHook 은 마감 해제 요청서가 최종 승인되면 같은 트랜잭션에서 달을 연다. 그 사이 이후 달이 마감되었으면 승인 전체가 취소된다(E12).
func releaseMonthHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	month, _ := rec.Fields["month"].(string)
	if _, err := parseMonthText(month); err != nil {
		return fmt.Errorf("%w: 해제할 월을 알 수 없는 요청서입니다", contract.ErrInvalidState)
	}
	repo := repository.SQLMonthClose{}
	cur, ok, err := repo.Get(ctx, tx, month)
	if err != nil {
		return err
	}
	if !ok || cur.Status != "closed" {
		return fmt.Errorf("%w: 이미 열려 있는 달이라 해제할 수 없습니다", contract.ErrInvalidState)
	}
	// 요청 당시의 마감 버전과 지금 버전이 다르면(그 사이 해제·재마감) 오래된 요청서라 승인 전체를 취소한다(검증자 B2).
	var reqVersion sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM month_close_events WHERE `+contract.SQLEntity(ctx, "")+` AND kind = 'release_request' AND doc_id = ? ORDER BY id DESC LIMIT 1`, rec.ID).Scan(&reqVersion); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if reqVersion.Valid && int(reqVersion.Int64) != cur.Version {
		return fmt.Errorf("%w: 이 요청서를 올린 뒤 마감이 다시 이루어졌습니다(요청 당시 %d차, 현재 %d차). 새로 해제를 요청하세요", contract.ErrInvalidState, reqVersion.Int64, cur.Version)
	}
	svc := &MonthCloseService{Base: b}
	if err := svc.latestClosedOnly(ctx, tx, month); err != nil {
		return fmt.Errorf("%w: 이후 달이 마감되어 해제할 수 없습니다", contract.ErrInvalidState)
	}
	now := b.now()
	if err := repo.SaveRelease(ctx, tx, month, rec.ID, now); err != nil {
		return err
	}
	if err := repo.AddEvent(ctx, tx, month, "release", cur.Version, p.UserID, rec.ID, now); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "month_close.release", TargetType: audit.TargetMonthClose, TargetID: month, Result: audit.ResultSuccess,
		Detail: "마감 해제", Diff: []audit.DiffItem{{Key: "doc_no", New: rec.No}, {Key: "version", New: fmt.Sprint(cur.Version)}}})
}

// rejectReleaseHook 은 마감 해제 요청서가 반려되면 사건과 감사를 남긴다(달은 마감 그대로다).
func rejectReleaseHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	month, _ := rec.Fields["month"].(string)
	if _, err := parseMonthText(month); err != nil {
		return nil
	}
	repo := repository.SQLMonthClose{}
	cur, _, err := repo.Get(ctx, tx, month)
	if err != nil {
		return err
	}
	if err := repo.AddEvent(ctx, tx, month, "release_reject", cur.Version, p.UserID, rec.ID, b.now()); err != nil {
		return err
	}
	return b.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "month_close.release_reject", TargetType: audit.TargetMonthClose, TargetID: month, Result: audit.ResultSuccess,
		Detail: "마감 해제 반려", Diff: []audit.DiffItem{{Key: "doc_no", New: rec.No}}})
}

// Status 는 한 달의 상태 요약이다(MCP 도구와 화면이 같이 쓴다). 금액은 재고 평가 조회 권한이 있을 때만 채운다.
func (s *MonthCloseService) Status(ctx context.Context, month string) (domain.MonthStatus, error) {
	p, err := contract.Authorize(ctx, contract.PermInventoryRead)
	if err != nil {
		return domain.MonthStatus{}, err
	}
	t, err := parseMonthText(month)
	if err != nil {
		return domain.MonthStatus{}, err
	}
	month = t.Format("2006-01")
	rec, have, err := s.Repo.Get(ctx, s.DB, month)
	if err != nil {
		return domain.MonthStatus{}, err
	}
	row, err := s.rowFor(ctx, s.DB, p, month, rec, have)
	if err != nil {
		return domain.MonthStatus{}, err
	}
	st := domain.MonthStatus{Month: month, State: row.State, Version: row.Version, ClosedAt: row.ClosedAt, CloseAmount: row.CloseAmount}
	if row.State == domain.MonthOpen {
		pc, err := s.precheck(ctx, s.DB, month)
		if err != nil && !errors.Is(err, contract.ErrValidation) {
			return st, err
		}
		st.Blockers, st.Warnings = len(pc.Blockers), len(pc.Warnings)
	}
	return st, nil
}
