package org

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 감사 동작 이름(명세 10절). 새 동작은 web/src/admin/auditLabels.ts 에 한국어 라벨이 있어야 한다.
var auditActions = map[string]string{
	KindUnitCreate: "org.unit_create", KindUnitRename: "org.unit_update", KindUnitMove: "org.unit_update", KindUnitSort: "org.unit_update",
	KindUnitClose: "org.unit_close", KindUnitReopen: "org.unit_reopen", KindHeadSet: "org.head_set", KindHeadClear: "org.head_clear",
	KindActingSet: "hr.acting_set", KindHire: "hr.hire", KindTransfer: "hr.transfer", KindRankChange: "hr.rank_change",
	KindConcurrentAdd: "hr.concurrent_add", KindConcurrentEnd: "hr.concurrent_end", KindLeave: "hr.leave",
	KindAbsenceStart: "org.absence_start", KindAbsenceEnd: "org.absence_end",
}

var kindNames = map[contract.OrgKind]string{
	contract.OrgCompany: "회사", contract.OrgDivision: "본부", contract.OrgDepartment: "부서", contract.OrgTeam: "팀",
}

func (s *Service) userName(ctx context.Context, q Querier, id int64) string {
	if id == 0 {
		return ""
	}
	var n string
	if err := q.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, id).Scan(&n); err != nil {
		return fmt.Sprintf("사용자 #%d", id)
	}
	return n
}

func (s *Service) rankName(ctx context.Context, q Querier, id int64) string {
	var n string
	if err := q.QueryRowContext(ctx, `SELECT name FROM org_ranks WHERE id = ?`, id).Scan(&n); err != nil {
		return ""
	}
	return n
}

func unitPath(sn *Snap, id int64) string {
	var us []contract.OrgUnit
	for _, u := range sn.Path(id) {
		us = append(us, contract.OrgUnit{ID: u.UnitID, Name: u.Name, Kind: u.Kind})
	}
	return contract.OrgPathText(us)
}

func unitLabel(sn *Snap, id int64) string {
	if p := unitPath(sn, id); p != "" {
		return p
	}
	if u, ok := sn.Units[id]; ok {
		return u.Name
	}
	return fmt.Sprintf("조직 #%d", id)
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return ""
}

// commandDiff 는 명령 한 건의 감사 변경 목록이다. 연락처·이메일은 넣지 않는다.
func (s *Service) commandDiff(ctx context.Context, q Querier, c Command, out Outcome, res *Result) ([]audit.DiffItem, audit.TargetType, string) {
	after := NewSnap(res, c.Effective)
	before := NewSnap(res, AddDays(c.Effective, -1))
	diff := []audit.DiffItem{{Key: "effective_date", New: string(c.Effective)}}
	target, id := audit.TargetPerson, fmt.Sprint(out.UserID)
	if isUnitKind(c.Kind) {
		target, id = audit.TargetOrgUnit, fmt.Sprint(out.UnitID)
	}
	add := func(key, old, nw string) {
		if old == nw && old != "" {
			return
		}
		diff = append(diff, audit.DiffItem{Key: key, Old: old, New: nw})
	}
	switch c.Kind {
	case KindUnitCreate:
		add("unit_code", "", c.Code)
		add("unit_name", "", strings.TrimSpace(c.Name))
		add("unit_kind", "", kindNames[c.UnitKind])
		add("parent", "", unitLabel(after, c.ParentID))
	case KindUnitRename:
		add("unit_name", before.Units[c.UnitID].Name, strings.TrimSpace(c.Name))
	case KindUnitMove:
		add("parent", unitLabel(before, before.Units[c.UnitID].ParentID), unitLabel(after, c.ParentID))
	case KindUnitSort:
		add("sort_order", fmt.Sprint(before.Units[c.UnitID].Sort), fmt.Sprint(c.Sort))
	case KindHeadSet:
		add("head", s.userName(ctx, q, before.Units[c.UnitID].HeadUser), s.userName(ctx, q, c.UserID))
	case KindHeadClear:
		add("head", s.userName(ctx, q, before.Units[c.UnitID].HeadUser), "")
	case KindActingSet:
		add("acting", "", s.userName(ctx, q, c.UserID))
		add("period_start", "", string(c.Effective))
		add("period_end", "", string(c.EndDay))
	case KindHire:
		add("employee_no", "", c.EmployeeNo)
		add("primary", "", unitLabel(after, c.ToUnit))
		add("rank", "", s.rankName(ctx, q, c.Rank))
		if c.UserID == 0 || c.Role != "" {
			add("role", "", string(c.Role))
		}
	case KindTransfer:
		add("primary", unitLabel(before, before.People[c.UserID].Primary), unitLabel(after, c.ToUnit))
	case KindRankChange:
		add("rank", s.rankName(ctx, q, before.People[c.UserID].Rank), s.rankName(ctx, q, c.Rank))
	case KindConcurrentAdd:
		add("concurrent", "", unitLabel(after, c.ToUnit))
	case KindConcurrentEnd:
		add("concurrent", unitLabel(before, c.ToUnit), "")
	case KindLeave:
		add("leave_kind", "", c.LeaveKind)
		add("last_day", "", string(c.LastDay))
	case KindAbsenceStart:
		// 사유 원문은 감사에 남기지 않는다(민감 정보). 길이만 남긴다(B50 15절).
		add("absence_kind", "", AbsenceLabel(c.AbsenceKind))
		add("return_day", "", string(c.ReturnDay))
		if n := utf8.RuneCountInString(strings.TrimSpace(c.AbsenceReason)); n > 0 {
			add("absence_reason_length", "", fmt.Sprint(n))
		}
	case KindAbsenceEnd:
		add("absence_kind", AbsenceLabel(before.People[c.UserID].Absence), "")
	}
	if r := strings.TrimSpace(c.Reason); r != "" {
		diff = append(diff, audit.DiffItem{Key: "reason", New: r})
	}
	if out.Retroactive {
		diff = append(diff, audit.DiffItem{Key: "retroactive", New: "예"})
	}
	return diff, target, id
}

func (s *Service) recordCommand(ctx context.Context, tx contract.Tx, actor contract.Principal, c Command, out Outcome, res *Result) error {
	diff, target, id := s.commandDiff(ctx, tx, c, out, res)
	if len(out.GainedRisk) > 0 { // role:manage 보유자가 위험 권한이 새로 생기는 발령을 반영했다
		diff = append(diff, audit.DiffItem{Key: "gained_risk", New: strings.Join(out.GainedRisk, ",")})
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: auditActions[c.Kind], TargetType: target, TargetID: id,
		Result: audit.ResultSuccess, Diff: diff})
}

func subjectTarget(e Event) (audit.TargetType, string) {
	if e.Subject == SubjectUnit {
		return audit.TargetOrgUnit, fmt.Sprint(e.SubjectID)
	}
	return audit.TargetPerson, fmt.Sprint(e.SubjectID)
}

func (s *Service) recordCancel(ctx context.Context, tx contract.Tx, actor contract.Principal, target Event, reason string, res *Result) error {
	tt, id := subjectTarget(target)
	diff := []audit.DiffItem{{Key: "target_event", New: fmt.Sprint(target.ID)}, {Key: "reason", New: strings.TrimSpace(reason)}}
	if target.Effective < s.Today() {
		diff = append(diff, audit.DiffItem{Key: "retroactive", New: "예"})
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "hr.event_cancel", TargetType: tt, TargetID: id, Result: audit.ResultSuccess, Diff: diff})
}

func (s *Service) recordCorrect(ctx context.Context, tx contract.Tx, actor contract.Principal, orig Event, c Command, out Outcome) error {
	tt, id := subjectTarget(orig)
	newID := ""
	if len(out.EventIDs) > 0 {
		newID = fmt.Sprint(out.EventIDs[0])
	}
	diff := []audit.DiffItem{{Key: "target_event", New: fmt.Sprint(orig.ID)}, {Key: "new_event", New: newID},
		{Key: "effective_date", Old: string(orig.Effective), New: string(c.Effective)}, {Key: "reason", New: strings.TrimSpace(c.Reason)}}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "hr.event_correct", TargetType: tt, TargetID: id, Result: audit.ResultSuccess, Diff: diff})
}

// pendingApprovals 는 이 사람이 지금 처리자(스냅샷 배정자 또는 재지정으로 바뀐 결재자)인 진행 중 결재 단계의 수다(B21).
// "단계의 현재 결재자"의 정의는 뷰 approval_step_current 한 곳이고, 조직 패키지는 서비스 패키지를 가져오지 않고(순환) 그 뷰만 읽는다.
// 재무 단계는 사람이 아니라 역할 풀이라 퇴사·전보로 멈추지 않으므로 세지 않는다. 위임 수임자는 배정자가 아니라 세지 않는다.
func pendingApprovals(ctx context.Context, q Querier, userID int64) int {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_steps s
		JOIN approval_step_current c ON c.pr_id = s.pr_id AND c.round = s.round AND c.seq = s.seq
		JOIN purchase_requests p ON p.id = s.pr_id AND p.round = s.round
		WHERE s.status = 'pending' AND c.user_id = ? AND p.status IN ('submitted','in_review')`, userID).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0
	}
	return n
}

// fillPreview 는 등록 뒤(롤백 전) 상태와 등록 전 상태를 비교해 영향 문구를 만든다.
func (s *Service) fillPreview(ctx context.Context, tx contract.Tx, pv *Preview, cmds []Command, outs []Outcome, before, after *Result) {
	for i, c := range cmds {
		pv.Retroactive = pv.Retroactive || outs[i].Retroactive
		pv.Warnings = append(pv.Warnings, outs[i].Warnings...)
		d := c.Effective
		b, a := NewSnap(before, d), NewSnap(after, d)
		if uid := outs[i].UserID; uid != 0 && !isUnitKind(c.Kind) {
			name := s.userName(ctx, tx, uid)
			bp, ap := b.People[uid], a.People[uid]
			switch {
			case c.Kind == KindLeave:
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s부터 퇴사 상태가 됩니다(마지막 근무일 %s).", name, DayText(d), DayText(c.LastDay)))
				pv.Effects = append(pv.Effects, "로그인 세션과 개인 토큰이 폐기되고 결재 위임이 해제됩니다.")
				pv.PendingApprovals += pendingApprovals(ctx, tx, uid)
				pv.addPendingDocs(ctx, tx, uid)
			case c.Kind == KindAbsenceStart:
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s부터 휴직 상태가 됩니다(%s%s).", name, DayText(d), AbsenceLabel(c.AbsenceKind), returnText(c.ReturnDay)))
				pv.Effects = append(pv.Effects, "휴직 중에도 로그인은 되지만 결재자·대결자·재무 담당으로는 지정·선택되지 않습니다. 소속·직위는 그대로입니다.")
				pv.PendingApprovals += pendingApprovals(ctx, tx, uid)
				pv.addPendingDocs(ctx, tx, uid)
			case c.Kind == KindAbsenceEnd:
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s부터 복직합니다. 결재 자격이 돌아옵니다.", name, DayText(d)))
			case c.Kind == KindHire && !b.Employed(uid):
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s부터 %s 소속으로 입사합니다.", name, DayText(d), unitLabel(a, ap.Primary)))
			case bp.Primary != ap.Primary && bp.Primary != 0:
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: 주소속이 %s에서 %s(으)로 바뀝니다.", name, unitLabel(b, bp.Primary), unitLabel(a, ap.Primary)))
				pv.PendingApprovals += pendingApprovals(ctx, tx, uid)
				pv.addPendingDocs(ctx, tx, uid)
			case bp.Rank != ap.Rank && bp.Rank != 0:
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: 직위가 %s에서 %s(으)로 바뀝니다.", name, s.rankName(ctx, tx, bp.Rank), s.rankName(ctx, tx, ap.Rank)))
			}
			if c.Kind == KindConcurrentAdd {
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s 겸직이 추가됩니다.", name, unitLabel(a, c.ToUnit)))
			}
			if c.Kind == KindConcurrentEnd {
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: %s 겸직이 해제됩니다.", name, unitLabel(b, c.ToUnit)))
			}
		}
		for id, au := range a.Units {
			bu, ok := b.Units[id]
			if !ok {
				if c.Kind == KindUnitCreate && id == outs[i].UnitID {
					pv.Effects = append(pv.Effects, fmt.Sprintf("조직 %s 이(가) 새로 만들어집니다.", unitLabel(a, id)))
				}
				continue
			}
			if bu.HeadUser != 0 && au.HeadUser == 0 && !bu.HeadActing {
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: 조직장(%s)이 없어져 공석이 됩니다.", unitLabel(a, id), s.userName(ctx, tx, bu.HeadUser)))
			} else if bu.HeadUser != au.HeadUser && au.HeadUser != 0 && c.Kind != KindActingSet {
				pv.Effects = append(pv.Effects, fmt.Sprintf("%s: 조직장이 %s에서 %s(으)로 바뀝니다.", unitLabel(a, id), s.userName(ctx, tx, bu.HeadUser), s.userName(ctx, tx, au.HeadUser)))
			}
			if bu.Name != au.Name || bu.ParentID != au.ParentID {
				pv.Effects = append(pv.Effects, fmt.Sprintf("조직 %s 이(가) %s 이(가) 됩니다.", unitLabel(b, id), unitLabel(a, id)))
			}
			if !bu.Closed && au.Closed {
				pv.Effects = append(pv.Effects, fmt.Sprintf("조직 %s 이(가) 폐지됩니다.", unitLabel(b, id)))
			}
		}
	}
	if len(pv.Effects) == 0 {
		pv.Effects = append(pv.Effects, "바뀌는 소속·직책이 없습니다.")
	}
}

func returnText(d contract.Day) string {
	if d == "" {
		return ""
	}
	return ", 예정 복직일 " + DayText(d)
}

// addPendingDocs 는 미리보기의 진행 중 건 목록에 이 사람의 문서를 더한다(전체 20건까지).
func (pv *Preview) addPendingDocs(ctx context.Context, q Querier, userID int64) {
	if len(pv.PendingDocs) >= 20 {
		return
	}
	docs, err := pendingDocs(ctx, q, userID, 20-len(pv.PendingDocs))
	if err != nil {
		return
	}
	pv.PendingDocs = append(pv.PendingDocs, docs...)
}

// recordDenied 는 거부된 시도를 별도 트랜잭션으로 남긴다(요청은 이미 실패했으므로 기록 오류는 삼킨다). 롤백된 트랜잭션 밖에서 부른다.
func (s *Service) recordDenied(ctx context.Context, actor contract.Principal, action string, tt audit.TargetType, id, detail string) {
	if actor.Channel == "" {
		actor.Channel = contract.ChannelWeb
	}
	_ = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: tt, TargetID: id, Result: audit.ResultDenied, Detail: detail})
	})
}

// denyRiskAttempt 는 위험 권한 때문에 거부된 발령 시도(위험 역할 지정·위험 권한이 새로 생기는 발령)를 거부 감사로 남긴다. 롤백된 트랜잭션 밖에서 부른다.
func (s *Service) denyRiskAttempt(ctx context.Context, actor contract.Principal, action string, err error) {
	var rr *access.RiskyRoleError
	var rg *RiskGainError
	switch {
	case errors.As(err, &rr):
		s.recordDenied(ctx, actor, action, audit.TargetPerson, "risky_role", "위험 권한이 든 역할 지정 시도")
	case errors.As(err, &rg):
		s.recordDenied(ctx, actor, action, audit.TargetPerson, "gained_risk", "gained_risk: "+strings.Join(rg.Perms, ","))
	}
}
