package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 휴가 신청(B50 6절). 신청은 결재 문서(양식 LEAVE) 위에 얹는다. 상신하면 잔여를 예약(원장 reserve)하고, 최종 승인에서 사용(use)으로 바꾸며,
// 반려·회수·취소에서 복원(release)한다. 결재선·위임·재지정·자기 결재 금지·이력 불변은 전자결재가 그대로 맡는다.

func isUniqueViolation(err error) bool {
	return dbutil.IsUnique(err) || (err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed"))
}

// leaveTransitions 는 휴가 신청의 허용 전이 표다(서비스 계층 한 곳). 표에 없는 전이는 전부 거부한다(명세 6.3). DB 트리거가 같은 규칙을 한 번 더 막는다.
var leaveTransitions = map[string]map[string]bool{
	domain.LeavePending:  {domain.LeaveApproved: true, domain.LeaveRejected: true, domain.LeaveRecalled: true},
	domain.LeaveApproved: {domain.LeaveCanceled: true},
}

// leaveMove 는 신청 상태를 표대로 바꾼다. 표에 없으면 ErrInvalidState, 같은 전이가 먼저 적용됐으면(경합) changed=false 다.
func (s *LeaveService) leaveMove(ctx context.Context, tx contract.Tx, id int64, from, to, note string) (bool, error) {
	if !leaveTransitions[from][to] {
		return false, fmt.Errorf("%w: %s 상태에서 %s 상태로 바꿀 수 없습니다", contract.ErrInvalidState, domain.LeaveStatusLabels[from], domain.LeaveStatusLabels[to])
	}
	return s.Repo.SetRequestStatus(ctx, tx, id, from, to, note, s.now())
}

// fmtMinutes 는 분을 "1일 2시간 30분" 꼴로 쓴다(하루 = wd 분).
func fmtMinutes(m int64, wd int) string {
	if wd <= 0 {
		wd = 480
	}
	d, rem := m/int64(wd), m%int64(wd)
	var parts []string
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%d일", d))
	}
	if h := rem / 60; h > 0 {
		parts = append(parts, fmt.Sprintf("%d시간", h))
	}
	if mm := rem % 60; mm > 0 {
		parts = append(parts, fmt.Sprintf("%d분", mm))
	}
	if len(parts) == 0 {
		return "0분"
	}
	return strings.Join(parts, " ")
}

// leaveCalc 는 Preview 와 Request 가 같이 쓰는 계산 결과다.
type leaveCalc struct {
	typ       domain.LeaveType
	policy    domain.LeavePolicy
	minutes   int64
	days      []domain.LeaveDay
	before    int64
	warnings  []string
	startDate string
	endDate   string
	part      string
}

func hhmm(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// calcLeave 는 신청 내용을 검사하고 사용 분량과 잔여 변화를 계산한다. 읽기만 한다(q 는 읽기 연결 또는 쓰기 트랜잭션).
func (s *LeaveService) calcLeave(ctx context.Context, q contract.Tx, p contract.Principal, in domain.LeaveInput) (leaveCalc, error) {
	var c leaveCalc
	today := string(contract.DayOf(s.now()))
	typ, err := s.Repo.TypeByCode(ctx, q, strings.TrimSpace(in.LeaveCode))
	if errors.Is(err, contract.ErrNotFound) || (err == nil && !typ.Active) {
		return c, validation("휴가 종류를 고르세요.")
	}
	if err != nil {
		return c, err
	}
	c.typ = typ
	pol, ok, err := s.Repo.PolicyOn(ctx, q, today)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, validation("휴가 규칙이 아직 설정되지 않았습니다. 관리자에게 문의하세요.")
	}
	c.policy = pol
	start, err1 := lvParseDay(in.StartDate)
	end, err2 := lvParseDay(in.EndDate)
	if err1 != nil || err2 != nil {
		return c, validation("휴가 기간을 YYYY-MM-DD 로 입력하세요.")
	}
	if end.Before(start) {
		return c, validation("종료일이 시작일보다 빠릅니다.")
	}
	if end.Sub(start) > 90*24*time.Hour {
		return c, validation("휴가는 한 번에 90일까지 신청할 수 있습니다.")
	}
	c.startDate, c.endDate = in.StartDate, in.EndDate
	part := in.Part
	if part == "" {
		part = domain.LeavePartFull
	}
	c.part = part
	switch part {
	case domain.LeavePartFull:
		if typ.Unit == "hour" {
			return c, validation("시간 단위 휴가는 시간을 지정해 신청하세요.")
		}
	case domain.LeavePartAM, domain.LeavePartPM, domain.LeavePartHours:
		if typ.Balance == "" && typ.Unit != "hour" {
			return c, validation("이 종류는 종일로만 신청할 수 있습니다.")
		}
		if in.StartDate != in.EndDate {
			return c, validation("반차·시간 단위 휴가는 하루만 신청할 수 있습니다.")
		}
	default:
		return c, validation("신청 구간을 고르세요.")
	}
	var perDay int64
	switch part {
	case domain.LeavePartFull:
		perDay = int64(pol.WorkdayMinutes)
	case domain.LeavePartAM, domain.LeavePartPM:
		perDay = int64(pol.WorkdayMinutes) / 2
	case domain.LeavePartHours:
		a, ok1 := hhmm(in.StartTime)
		b, ok2 := hhmm(in.EndTime)
		if !ok1 || !ok2 || b <= a {
			return c, validation("시간을 HH:MM 형식으로, 끝이 시작보다 늦게 입력하세요.")
		}
		perDay = int64(b - a)
		if perDay%int64(pol.HourUnitMinutes) != 0 {
			return c, validation("%s", fmt.Sprintf("시간은 %d분 단위로 입력하세요.", pol.HourUnitMinutes))
		}
		if perDay >= int64(pol.WorkdayMinutes) {
			return c, validation("하루 이상은 종일로 신청하세요.")
		}
	}
	// 소급은 사유 필수이고 30일까지다(명세 6.2 6번, D10).
	if in.StartDate < today {
		if strings.TrimSpace(in.Reason) == "" {
			return c, validation("지난 날짜의 휴가는 사유를 입력해야 신청할 수 있습니다.")
		}
		limit := lvFmtDay(time.Now().In(contract.KST).AddDate(0, 0, -30))
		if in.StartDate < limit {
			return c, validation("지난 날짜의 휴가는 30일 전까지만 신청할 수 있습니다. 인사 담당에게 문의하세요.")
		}
	}
	// 휴직 중인 날은 신청할 수 없다(명세 6.2 의 3, 8.3 의 6). 휴직은 조직 발령(B20)이 정한 구간이다.
	if spans, err := s.Repo.AbsenceSpans(ctx, q, p.UserID, in.StartDate, in.EndDate); err != nil {
		return c, err
	} else if len(spans) > 0 {
		return c, validation("%s", "휴직 기간("+absenceSpanText(spans[0])+")에는 신청할 수 없습니다.")
	}
	// 근태 마감된 달의 소급 신청은 인사 담당만 한다(명세 6.2 6번).
	if in.StartDate < today && !p.Can(contract.PermAttendanceHR) {
		if m, err := s.closedMonthIn(ctx, q, contract.WriteEntityID(ctx), in.StartDate, minStr(in.EndDate, today)); err != nil {
			return c, err
		} else if m != "" {
			return c, validation("%s", msgClosedMonth)
		}
	}
	workdays := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		ds := lvFmtDay(d)
		biz, err := s.isBusinessDay(ctx, q, ds)
		if err != nil {
			return c, err
		}
		if !biz {
			c.days = append(c.days, domain.LeaveDay{Date: ds, Skipped: "주말·공휴일"})
			continue
		}
		workdays++
		c.days = append(c.days, domain.LeaveDay{Date: ds, Minutes: perDay})
		c.minutes += perDay
	}
	if c.minutes == 0 {
		return c, validation("선택한 기간이 모두 휴일입니다.")
	}
	if typ.MaxDays > 0 && workdays > typ.MaxDays {
		return c, validation("%s", fmt.Sprintf("%s 은(는) 최대 %d일까지 신청할 수 있습니다.", typ.Name, typ.MaxDays))
	}
	if typ.ProofRequired && in.ProofAttachmentID == 0 {
		return c, validation("이 휴가는 증빙 첨부가 필요합니다.")
	}
	if utf8.RuneCountInString(in.Reason) > 200 {
		return c, validation("사유는 200자 이내로 입력하세요.")
	}
	// 같은 날 이미 신청한 휴가와 하루 분량을 넘지 않아야 한다(반차 오전+오후는 되고, 겹치는 종일은 거부).
	existing, err := s.Repo.ActiveRequestsBetween(ctx, q, p.UserID, in.StartDate, in.EndDate)
	if err != nil {
		return c, err
	}
	loads := map[string]int64{}
	for _, r := range existing {
		rs, _ := lvParseDay(r.StartDate)
		re, _ := lvParseDay(r.EndDate)
		for d := rs; !d.After(re); d = d.AddDate(0, 0, 1) {
			ds := lvFmtDay(d)
			if r.StartDate == r.EndDate {
				loads[ds] += r.Minutes
				continue
			}
			if biz, _ := s.isBusinessDay(ctx, q, ds); biz {
				loads[ds] += int64(pol.WorkdayMinutes)
			}
		}
	}
	for _, d := range c.days {
		if d.Skipped == "" && loads[d.Date]+d.Minutes > int64(pol.WorkdayMinutes) {
			return c, validation("%s", fmt.Sprintf("%s 에 이미 신청한 휴가가 있어 하루 분량을 넘습니다.", d.Date))
		}
	}
	if typ.Balance != "" {
		total, _, err := s.Repo.BalanceMinutes(ctx, q, p.UserID, typ.Balance)
		if err != nil {
			return c, err
		}
		c.before = total
		if c.minutes > total {
			return c, validation("%s", fmt.Sprintf("잔여 %s(%s)보다 많은 %s 을(를) 신청할 수 없습니다.", domain.LeaveBalanceLabels[typ.Balance], fmtMinutes(total, pol.WorkdayMinutes), fmtMinutes(c.minutes, pol.WorkdayMinutes)))
		}
	}
	return c, nil
}

func leavePeriodText(a, b string) string {
	if a == b {
		return a
	}
	return a + " ~ " + b
}

func leaveAmountText(c leaveCalc) string {
	switch c.part {
	case domain.LeavePartAM:
		return "오전 반차(" + fmtMinutes(c.minutes, c.policy.WorkdayMinutes) + ")"
	case domain.LeavePartPM:
		return "오후 반차(" + fmtMinutes(c.minutes, c.policy.WorkdayMinutes) + ")"
	}
	return fmtMinutes(c.minutes, c.policy.WorkdayMinutes)
}

// Preview 는 신청 전 계산이다(Request 가 같은 함수를 쓴다). 아무것도 쓰지 않는다.
func (s *LeaveService) Preview(ctx context.Context, in domain.LeaveInput) (domain.LeavePreview, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.LeavePreview{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.LeavePreview{}, err
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Repo.EnsureTypes(ctx, tx, contract.WriteEntityID(ctx), s.now()) }); err != nil {
		return domain.LeavePreview{}, err
	}
	if err := s.checkDelegate(ctx, p, in.DelegateUserID); err != nil {
		return domain.LeavePreview{}, err
	}
	c, err := s.calcLeave(ctx, s.DB, p, in)
	if err != nil {
		return domain.LeavePreview{}, err
	}
	out := domain.LeavePreview{Minutes: c.minutes, Days: c.days, Balance: c.typ.Balance, BalanceBefore: c.before, WorkdayMinutes: c.policy.WorkdayMinutes, Warnings: c.warnings}
	if c.typ.Balance != "" {
		out.BalanceAfter = c.before - c.minutes
	}
	return out, nil
}

// Request 는 휴가를 신청한다: 문서 작성 → (한 트랜잭션) 잔여 확인·신청 행·예약 → 상신. 상신이 실패하면 예약을 되돌린다.
func (s *LeaveService) Request(ctx context.Context, in domain.LeaveInput) (domain.LeaveRequest, error) {
	p, err := s.authorize(ctx, contract.PermAttendanceSelf, "leave.request", audit.TargetLeaveRequest)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.LeaveRequest{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.LeaveRequest{}, err
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Repo.EnsureTypes(ctx, tx, contract.WriteEntityID(ctx), s.now()) }); err != nil {
		return domain.LeaveRequest{}, err
	}
	if err := s.checkDelegate(ctx, p, in.DelegateUserID); err != nil {
		return domain.LeaveRequest{}, err
	}
	if in.ProofAttachmentID != 0 {
		if ok, err := s.Repo.ProofOwnedFree(ctx, s.DB, in.ProofAttachmentID, p.UserID); err != nil {
			return domain.LeaveRequest{}, err
		} else if !ok {
			return domain.LeaveRequest{}, validation("증빙 첨부를 찾을 수 없습니다. 파일을 다시 올려 주세요.")
		}
	}
	c, err := s.calcLeave(ctx, s.DB, p, in)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	dctx := withSystemForm(ctx)
	title := fmt.Sprintf("휴가 신청 · %s %s", c.typ.Name, leavePeriodText(in.StartDate, in.EndDate))
	doc, err := s.Docs.CreateDraft(dctx, domain.DocDraftInput{FormCode: "LEAVE", Title: title, Fields: map[string]any{"leave_name": c.typ.Name, "period": leavePeriodText(in.StartDate, in.EndDate), "amount": leaveAmountText(c)}})
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	var reqID int64
	entity := contract.WriteEntityID(ctx)
	err = s.write(ctx, p, "leave.request", audit.TargetLeaveRequest, 0, func(tx contract.Tx) error {
		// 한 사람의 잔여 읽기·예약을 직렬화한 뒤 다시 계산한다(동시 신청 두 건이 잔여를 함께 넘지 못한다).
		if err := s.Repo.LockUser(ctx, tx, p.UserID); err != nil {
			return err
		}
		c2, err := s.calcLeave(ctx, tx, p, in)
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertRequest(ctx, tx, repository.LeaveReqRow{LeaveRequest: domain.LeaveRequest{UserID: p.UserID, DocID: doc.ID, LeaveCode: c2.typ.Code, StartDate: in.StartDate, EndDate: in.EndDate,
			Part: c2.part, StartTime: in.StartTime, EndTime: in.EndTime, Minutes: c2.minutes, Status: domain.LeavePending, Reason: strings.TrimSpace(in.Reason), DelegateUserID: in.DelegateUserID, ProofAttachmentID: in.ProofAttachmentID}, Balance: c2.typ.Balance}, entity, s.now())
		if err != nil {
			return err
		}
		reqID = id
		if in.ProofAttachmentID != 0 { // 증빙을 결재 문서에 붙인다(문서가 임시저장인 지금만 연결이 바뀐다)
			if ok, err := s.Repo.LinkProof(ctx, tx, in.ProofAttachmentID, p.UserID, doc.ID); err != nil {
				return err
			} else if !ok {
				return validation("증빙 첨부를 찾을 수 없습니다. 파일을 다시 올려 주세요.")
			}
		}
		if c2.typ.Balance != "" {
			if _, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: entity, UserID: p.UserID, Balance: c2.typ.Balance, Kind: "reserve", Minutes: -c2.minutes,
				RefType: "leave_request", RefID: id, Reason: "휴가 신청 예약", By: p.UserID, At: s.now()}); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "leave.request", audit.TargetLeaveRequest, id, audit.DiffItem{Key: "leave_code", New: c2.typ.Code},
			audit.DiffItem{Key: "period", New: leavePeriodText(in.StartDate, in.EndDate)}, audit.DiffItem{Key: "minutes", New: fmt.Sprint(c2.minutes)},
			audit.DiffItem{Key: "balance_before", New: fmt.Sprint(c2.before)}, audit.DiffItem{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(strings.TrimSpace(in.Reason)))})
	})
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	if _, err := s.Docs.Submit(dctx, doc.ID); err != nil {
		// 상신이 실패했다(결재선 없음 등): 예약을 풀고 신청을 회수 상태로 둔다. 문서는 임시저장으로 남는다.
		_ = s.write(ctx, p, "leave.recall", audit.TargetLeaveRequest, reqID, func(tx contract.Tx) error {
			return s.releaseRequest(ctx, tx, p, reqID, domain.LeaveRecalled, "상신 실패")
		})
		return domain.LeaveRequest{}, err
	}
	return s.Get(ctx, reqID)
}

// releaseRequest 는 대기 중인 신청을 끝내고(반려·회수) 예약을 푼다. 같은 전이가 먼저 적용됐으면 아무것도 하지 않는다.
func (s *LeaveService) releaseRequest(ctx context.Context, tx contract.Tx, p contract.Principal, id int64, to, reason string) error {
	row, err := s.Repo.RequestByID(contractEntityAll(ctx), tx, id)
	if err != nil {
		return err
	}
	changed, err := s.leaveMove(ctx, tx, id, domain.LeavePending, to, reason)
	if err != nil || !changed {
		return err
	}
	if row.Balance != "" {
		if _, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: row.EntityID, UserID: row.UserID, Balance: row.Balance, Kind: "release", Minutes: row.Minutes,
			RefType: "leave_request", RefID: id, Reason: "복원: " + reason, By: p.UserID, At: s.now()}); err != nil {
			return err
		}
	}
	action := map[string]string{domain.LeaveRejected: "leave.reject", domain.LeaveRecalled: "leave.recall"}[to]
	return s.record(ctx, tx, p, action, audit.TargetLeaveRequest, id, audit.DiffItem{Key: "minutes", New: fmt.Sprint(row.Minutes)})
}

// contractEntityAll 은 훅처럼 현재 법인이 정해지지 않은 곳에서 법인 조건 없이 읽게 전체 모드 context 를 만든다.
func contractEntityAll(ctx context.Context) context.Context {
	return contract.WithActiveEntity(ctx, contract.ActiveEntity{All: true})
}

// approveRequest 는 최종 승인에서 예약을 사용으로 바꾼다(release + use 두 줄, 합은 그대로 −분량).
func (s *LeaveService) approveRequest(ctx context.Context, tx contract.Tx, p contract.Principal, id int64) error {
	row, err := s.Repo.RequestByID(contractEntityAll(ctx), tx, id)
	if err != nil {
		return err
	}
	changed, err := s.leaveMove(ctx, tx, id, domain.LeavePending, domain.LeaveApproved, "")
	if err != nil || !changed {
		return err
	}
	if row.Balance != "" {
		for _, e := range []repository.LeaveLedgerEntry{
			{Kind: "release", Minutes: row.Minutes, Reason: "예약 해제(승인)"},
			{Kind: "use", Minutes: -row.Minutes, Reason: "휴가 사용"},
		} {
			e.EntityID, e.UserID, e.Balance, e.RefType, e.RefID, e.By, e.At = row.EntityID, row.UserID, row.Balance, "leave_request", id, p.UserID, s.now()
			if _, err := s.Repo.Append(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	diff := []audit.DiffItem{{Key: "minutes", New: fmt.Sprint(row.Minutes)}}
	// 대결자를 적었으면 부재 설정(B29)을 만든다. 만들지 못해도 승인은 그대로이고 안내 문구만 신청에 남는다.
	absID, absNote, err := s.createAbsence(ctx, tx, row)
	if err != nil {
		return err
	}
	if absID != 0 || absNote != "" {
		if err := s.Repo.SetAbsence(ctx, tx, id, absID, absNote, s.now()); err != nil {
			return err
		}
		if absNote != "" {
			diff = append(diff, audit.DiffItem{Key: "absence", New: absNote})
		}
	}
	if !s.leaveModuleOnTx(ctx, tx) {
		diff = append(diff, audit.DiffItem{Key: "note", New: "근태·휴가 모듈이 꺼져 있는 동안 승인됨"})
	}
	if err := s.notifyClosedMonthAdded(ctx, tx, row); err != nil {
		return err
	}
	return s.record(ctx, tx, p, "leave.approve", audit.TargetLeaveRequest, id, diff...)
}

// 결재 훅(approval_hooks.go 의 docHookTable["LEAVE"]): 같은 트랜잭션에서 신청 상태와 원장을 바꾼다.
func leaveService(b Base) *LeaveService { return &LeaveService{Base: b} }

func approveLeaveHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	s := leaveService(b)
	row, ok, err := s.Repo.RequestByDocID(ctx, tx, rec.ID)
	if err != nil || !ok {
		return err
	}
	return s.approveRequest(ctx, tx, p, row.ID)
}

func rejectLeaveHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord, reason string) error {
	s := leaveService(b)
	row, ok, err := s.Repo.RequestByDocID(ctx, tx, rec.ID)
	if err != nil || !ok {
		return err
	}
	return s.releaseRequest(ctx, tx, p, row.ID, domain.LeaveRejected, "반려")
}

func resetLeaveHook(ctx context.Context, b Base, tx contract.Tx, p contract.Principal, rec repository.PRRecord) error {
	s := leaveService(b)
	row, ok, err := s.Repo.RequestByDocID(ctx, tx, rec.ID)
	if err != nil || !ok {
		return err
	}
	return s.releaseRequest(ctx, tx, p, row.ID, domain.LeaveRecalled, "상신 취소")
}

// canViewUser 는 호출자가 대상 사람의 휴가 정보를 볼 수 있는지다(명세 10.2): 본인, 인사 담당(현재 법인), 직속 조직장(자기 조직·하위 소속원).
func (s *LeaveService) canViewUser(ctx context.Context, p contract.Principal, target int64) (bool, error) {
	if target == p.UserID {
		return true, nil
	}
	day := contract.DayOf(s.now())
	if p.Can(contract.PermAttendanceHR) {
		return s.userInEntity(ctx, target, contract.WriteEntityID(ctx))
	}
	if p.Can(contract.PermAttendanceTeam) && s.Org != nil {
		units, err := s.Org.HeadedScope(ctx, p.UserID, day)
		if err != nil {
			return false, err
		}
		person, err := s.Org.Person(ctx, target, day)
		if err != nil {
			return false, nil // 조직에 없는 사람은 볼 수 없다
		}
		in := map[int64]bool{}
		for _, u := range units {
			in[u] = true
		}
		return in[person.Primary.Unit.ID], nil
	}
	return false, nil
}

// Balance 는 사람의 잔여다. userID 0 은 본인. 범위 밖 사람은 없는 것과 같은 ErrNotFound 다.
func (s *LeaveService) Balance(ctx context.Context, userID int64) (domain.LeaveBalance, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.LeaveBalance{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.LeaveBalance{}, err
	}
	if userID == 0 {
		userID = p.UserID
	}
	if ok, err := s.canViewUser(ctx, p, userID); err != nil {
		return domain.LeaveBalance{}, err
	} else if !ok {
		return domain.LeaveBalance{}, contract.ErrNotFound
	}
	today := string(contract.DayOf(s.now()))
	pol, okPol, err := s.Repo.PolicyOn(ctx, s.DB, today)
	if err != nil {
		return domain.LeaveBalance{}, err
	}
	wd := 480
	if okPol {
		wd = pol.WorkdayMinutes
	}
	out := domain.LeaveBalance{UserID: userID, WorkdayMinutes: wd}
	soon := lvFmtDay(time.Now().In(contract.KST).AddDate(0, 0, 30))
	for _, b := range []string{domain.LeaveBalanceAnnual, domain.LeaveBalanceCompensatory} {
		total, reserved, err := s.Repo.BalanceMinutes(ctx, s.DB, userID, b)
		if err != nil {
			return out, err
		}
		line := domain.LeaveBalanceLine{Balance: b, Minutes: total, Reserved: reserved}
		grants, err := s.Repo.ExpiringGrants(ctx, s.DB, userID, b)
		if err != nil {
			return out, err
		}
		consumed, err := s.Repo.ConsumedMinutes(ctx, s.DB, userID, b)
		if err != nil {
			return out, err
		}
		rem := fifoRemaining(grants, consumed)
		for i, g := range grants {
			if g.ExpiresOn <= soon && rem[i] > 0 && g.ExpiresOn > today {
				line.ExpiringMinutes += rem[i]
				if line.ExpiringOn == "" {
					line.ExpiringOn = g.ExpiresOn
				}
			}
		}
		out.Lines = append(out.Lines, line)
	}
	return out, nil
}

// Ledger 는 사람의 잔여 원장(최근 순)이다. 열람 범위는 Balance 와 같다.
func (s *LeaveService) Ledger(ctx context.Context, userID int64, limit int) ([]domain.LeaveLedgerLine, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, err
	}
	if userID == 0 {
		userID = p.UserID
	}
	if ok, err := s.canViewUser(ctx, p, userID); err != nil {
		return nil, err
	} else if !ok {
		return nil, contract.ErrNotFound
	}
	return s.Repo.LedgerLines(ctx, s.DB, userID, limit)
}

// Adjust 는 인사 담당의 조정·초기 이관이다(사유 필수, 본인 잔여는 본인이 조정할 수 없다). 원장에 줄을 더할 뿐 합계를 직접 고치지 않는다.
func (s *LeaveService) Adjust(ctx context.Context, in domain.LeaveAdjustInput) error {
	p, err := s.authorize(ctx, contract.PermAttendanceHR, "leave.adjust", audit.TargetLeaveRequest)
	if err != nil {
		return err
	}
	if err := s.requireModule(ctx); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(in.Reason); n < 1 || n > 200 {
		return validation("조정 사유를 1~200자로 입력하세요.")
	}
	if in.UserID == p.UserID {
		s.recordDenied(ctx, p, "leave.adjust", audit.TargetLeaveRequest, in.UserID, "본인 잔여 조정")
		return validation("본인의 잔여는 본인이 조정할 수 없습니다. 다른 인사 담당자나 관리자에게 요청하세요.")
	}
	if in.Balance != domain.LeaveBalanceAnnual && in.Balance != domain.LeaveBalanceCompensatory {
		return validation("조정할 잔여를 연차·대체휴가 중에서 고르세요.")
	}
	if in.Kind != "adjust" && in.Kind != "opening" {
		return validation("조정 종류를 고르세요.")
	}
	if in.Minutes == 0 || in.Minutes > 100000 || in.Minutes < -100000 {
		return validation("조정 분량이 올바르지 않습니다.")
	}
	if in.ExpiresOn != "" {
		if _, err := lvParseDay(in.ExpiresOn); err != nil || in.Minutes < 0 {
			return validation("소멸일은 양수로 늘리는 줄에만 YYYY-MM-DD 로 입력할 수 있습니다.")
		}
	}
	entity := contract.WriteEntityID(ctx)
	if in, err := s.userInEntity(ctx, in.UserID, entity); err != nil {
		return err
	} else if !in {
		return contract.ErrNotFound
	}
	action := "leave.adjust"
	if in.Kind == "opening" {
		action = "leave.opening"
	}
	return s.write(ctx, p, action, audit.TargetLeaveRequest, in.UserID, func(tx contract.Tx) error {
		if err := s.Repo.LockUser(ctx, tx, in.UserID); err != nil {
			return err
		}
		before, _, err := s.Repo.BalanceMinutes(ctx, tx, in.UserID, in.Balance)
		if err != nil {
			return err
		}
		if _, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: entity, UserID: in.UserID, Balance: in.Balance, Kind: in.Kind, Minutes: in.Minutes,
			ExpiresOn: in.ExpiresOn, Reason: in.Reason, By: p.UserID, At: s.now()}); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetLeaveRequest, in.UserID, audit.DiffItem{Key: "balance", New: domain.LeaveBalanceLabels[in.Balance]},
			audit.DiffItem{Key: "balance_before", New: fmt.Sprint(before)}, audit.DiffItem{Key: "balance_after", New: fmt.Sprint(before + in.Minutes)},
			audit.DiffItem{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(in.Reason))})
	})
}

// ErrLeavePrivacyNote 는 사유 원문 열람 기록을 남기지 못해 사유를 보여 줄 수 없다는 뜻이다(fail-closed).
var ErrLeavePrivacyNote = errors.New("열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요")

// view 는 응답용 신청이다. 사유 원문은 본인·직속 조직장·인사 담당만 보고, 본인이 아닌 사람에게 실리기 전에 B41 열람 기록(범주 attendance)을 남긴다.
// 기록하지 못하면 사유 없이 실패로 응답한다(fail-closed). 그 밖의 사람에게는 "사유 있음"(HasReason)만 보인다.
func (s *LeaveService) view(ctx context.Context, p contract.Principal, r repository.LeaveReqRow) (domain.LeaveRequest, error) {
	out := r.LeaveRequest
	if r.UserID == p.UserID || r.Reason == "" {
		return out, nil
	}
	if ok, err := s.canViewUser(ctx, p, r.UserID); err != nil || !ok {
		out.Reason = ""
		return out, err
	}
	if s.Privacy == nil {
		return domain.LeaveRequest{}, ErrLeavePrivacyNote
	}
	if err := s.Privacy.Note(ctx, p, privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason, Fields: []string{privacy.FieldLeaveReason},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: r.UserID}}}); err != nil {
		log.Printf("leave: 열람 기록 실패: %v", err)
		return domain.LeaveRequest{}, ErrLeavePrivacyNote
	}
	return out, nil
}

// MyRequests 는 본인 신청 목록이다.
func (s *LeaveService) MyRequests(ctx context.Context, limit int) ([]domain.LeaveRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, err
	}
	rows, err := s.Repo.UserRequests(ctx, s.DB, p.UserID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.LeaveRequest, len(rows))
	for i, r := range rows {
		if out[i], err = s.view(ctx, p, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get 은 신청 한 건이다. 열람 범위 밖은 없는 것과 같은 ErrNotFound 다.
func (s *LeaveService) Get(ctx context.Context, id int64) (domain.LeaveRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermAttendanceSelf)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.LeaveRequest{}, err
	}
	r, err := s.Repo.RequestByID(ctx, s.DB, id)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	if ok, err := s.canViewUser(ctx, p, r.UserID); err != nil {
		return domain.LeaveRequest{}, err
	} else if !ok {
		return domain.LeaveRequest{}, contract.ErrNotFound
	}
	return s.view(ctx, p, r)
}

// Cancel 은 신청을 취소한다. 신청 대기는 상신 취소(회수), 승인된 휴가는 시작 전에 본인이 즉시 취소한다(재결재 없음). 시작된 뒤에는 인사 담당만 취소한다(사유 필수).
func (s *LeaveService) Cancel(ctx context.Context, id int64, reason string) (domain.LeaveRequest, error) {
	p, err := s.authorize(ctx, contract.PermAttendanceSelf, "leave.cancel", audit.TargetLeaveRequest)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	if err := s.requireModule(ctx); err != nil {
		return domain.LeaveRequest{}, err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 200 {
		return domain.LeaveRequest{}, validation("취소 사유는 200자 이내로 입력하세요.")
	}
	cur, err := s.Repo.RequestByID(ctx, s.DB, id)
	if err != nil {
		return domain.LeaveRequest{}, err
	}
	isHR := p.Can(contract.PermAttendanceHR)
	if cur.UserID != p.UserID {
		if !isHR {
			return domain.LeaveRequest{}, contract.ErrNotFound
		}
		if reason == "" {
			return domain.LeaveRequest{}, validation("인사 담당이 취소하려면 사유를 입력해야 합니다.")
		}
	}
	switch cur.Status {
	case domain.LeavePending:
		if cur.UserID != p.UserID {
			return domain.LeaveRequest{}, fmt.Errorf("%w: 신청 대기 중인 휴가는 신청자가 상신을 취소해야 합니다", contract.ErrInvalidState)
		}
		if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
			return domain.LeaveRequest{}, err
		}
		if _, err := s.Docs.CancelSubmit(withSystemForm(ctx), cur.DocID, "휴가 신청 취소"); err != nil {
			return domain.LeaveRequest{}, err
		}
		// 회수 훅이 예약을 풀고 상태를 회수로 바꿨다.
	case domain.LeaveApproved:
		if cur.StartDate <= string(contract.DayOf(s.now())) && !isHR {
			return domain.LeaveRequest{}, fmt.Errorf("%w: 휴가가 시작되어 직접 취소할 수 없습니다. 인사 담당에게 취소를 요청하세요", contract.ErrInvalidState)
		}
		err = s.write(ctx, p, "leave.cancel", audit.TargetLeaveRequest, id, func(tx contract.Tx) error {
			if err := s.Repo.LockUser(ctx, tx, cur.UserID); err != nil {
				return err
			}
			changed, err := s.leaveMove(ctx, tx, id, domain.LeaveApproved, domain.LeaveCanceled, reason)
			if err != nil {
				return err
			}
			if !changed {
				return fmt.Errorf("%w: 이미 취소되었습니다", contract.ErrInvalidState)
			}
			if cur.Balance != "" {
				// 휴직 발령으로 이미 복원한 분량은 빼고 복원한다(두 번 돌려주지 않는다, 명세 16절).
				restored, err := s.Repo.AbsenceRestoredMinutes(contractEntityAll(ctx), tx, id)
				if err != nil {
					return err
				}
				if back := cur.Minutes - restored; back > 0 {
					if _, err := s.Repo.Append(ctx, tx, repository.LeaveLedgerEntry{EntityID: cur.EntityID, UserID: cur.UserID, Balance: cur.Balance, Kind: "release", Minutes: back,
						RefType: "leave_request", RefID: id, Reason: "복원: 취소", By: p.UserID, At: s.now()}); err != nil {
						return err
					}
				}
			}
			if err := s.revokeAbsence(ctx, tx, cur); err != nil { // 승인 때 만든 부재(대결) 설정도 함께 해제한다
				return err
			}
			return s.record(ctx, tx, p, "leave.cancel", audit.TargetLeaveRequest, id, audit.DiffItem{Key: "minutes", New: fmt.Sprint(cur.Minutes)},
				audit.DiffItem{Key: "reason_len", New: fmt.Sprintf("%d자", utf8.RuneCountInString(reason))})
		})
		if err != nil {
			return domain.LeaveRequest{}, err
		}
	default:
		return domain.LeaveRequest{}, fmt.Errorf("%w: 이미 끝난 신청이라 취소할 수 없습니다(%s)", contract.ErrInvalidState, domain.LeaveStatusLabels[cur.Status])
	}
	return s.Get(ctx, id)
}

// userInEntity 는 사람이 그 법인 소속인지다: 조직 소속 법인과 같거나, 조직에 등록되지 않았어도 활성 법인이 하나뿐이면 그 법인이다.
func (s *LeaveService) userInEntity(ctx context.Context, userID, entity int64) (bool, error) {
	ent, ok, err := s.Repo.UserEntity(ctx, s.DB, userID, string(contract.DayOf(s.now())))
	if err != nil {
		return false, err
	}
	if ok {
		return ent == entity, nil
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE active = 1`).Scan(&n); err != nil {
		return false, err
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return false, err
	}
	return n <= 1 && exists == 1, nil
}

// absenceSpanText 는 휴직 구간을 "11. 3.~12. 2." 로 쓴다(끝이 열려 있으면 "11. 3.~").
func absenceSpanText(sp repository.AbsenceSpan) string {
	short := func(day string) string {
		t, err := lvParseDay(day)
		if err != nil {
			return day
		}
		return fmt.Sprintf("%d. %d.", int(t.Month()), t.Day())
	}
	if sp.To == "" {
		return short(sp.From) + "~"
	}
	t, err := lvParseDay(sp.To)
	if err != nil {
		return short(sp.From) + "~"
	}
	return short(sp.From) + "~" + short(lvFmtDay(t.AddDate(0, 0, -1)))
}

func minStr(a, b string) string {
	if a < b {
		return a
	}
	return b
}

// closedMonthIn 은 [from, to] 에 걸친 달 중 근태 마감된 첫 달(YYYY-MM)이다. 없으면 "".
func (s *LeaveService) closedMonthIn(ctx context.Context, q contract.Tx, entity int64, from, to string) (string, error) {
	a, err1 := lvParseDay(from)
	b, err2 := lvParseDay(to)
	if err1 != nil || err2 != nil || b.Before(a) {
		return "", nil
	}
	att := repository.SQLAttendance{}
	for m := time.Date(a.Year(), a.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(b); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		closed, err := att.MonthClosed(ctx, q, entity, key)
		if err != nil {
			return "", err
		}
		if closed {
			return key, nil
		}
	}
	return "", nil
}

// notifyClosedMonthAdded 는 근태 마감된 달에 휴가가 승인되면 인사 담당에게 알린다(명세 16절: 승인은 되고 마감 내용은 바뀌지 않는다).
func (s *LeaveService) notifyClosedMonthAdded(ctx context.Context, tx contract.Tx, row repository.LeaveReqRow) error {
	today := string(contract.DayOf(s.now()))
	if row.StartDate >= today {
		return nil
	}
	m, err := s.closedMonthIn(ctx, tx, row.EntityID, row.StartDate, minStr(row.EndDate, today))
	if err != nil || m == "" {
		return err
	}
	hr, err := repository.SQLAttendance{}.HRUsers(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: row.EntityID}), tx)
	if err != nil {
		return err
	}
	for _, id := range hr {
		if err := s.notify(ctx, tx, repository.NotificationInsert{UserID: id, Kind: "leave_month_closed_added", Title: "마감된 달에 휴가가 추가되었습니다",
			Body: fmt.Sprintf("근태 마감된 %s 에 %s ~ %s 휴가가 승인되었습니다. 마감 내용은 바뀌지 않았으니 필요하면 확인하세요.", m, row.StartDate, row.EndDate), Link: "/attendance/admin", RefType: "leave_request", RefID: row.ID}); err != nil {
			return err
		}
	}
	return nil
}
