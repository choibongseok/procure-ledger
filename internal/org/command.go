package org

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Command 는 관리자가 등록하는 조직 변경·인사 발령 한 건이다. Kind 는 이벤트 종류와 같다(Kind* 상수).
// 종류에 필요한 칸만 채운다. 서비스가 검증해 이벤트로 바꾼다.
type Command struct {
	Kind      string
	Effective contract.Day // 비면 오늘. 퇴사는 LastDay 다음 날로 정해진다
	Reason    string
	Confirm   bool // 30일을 넘는 소급 발령을 확인했는가
	// trustedRole 은 개발용 시드와 시험이 위험 권한이 든 역할로 입사시킬 때만 패키지 안에서 켠다. 요청(HTTP·CSV)으로는 켤 수 없다(소문자 필드).
	trustedRole bool

	// 조직 변경
	UnitID   int64 // 대상 조직(신설이면 비움)
	Code     string
	Name     string
	UnitKind contract.OrgKind
	ParentID int64
	Sort     int
	// FixedUnitID 는 신설할 조직의 번호를 정한다(기존 부서 번호를 이어 쓰는 시드·백필 전용). HTTP 로는 받지 않는다.
	FixedUnitID int64

	// 인사 발령
	UserID     int64 // 대상 사람(입사는 비우면 새 사람, 채우면 재입사). 조직장 보임·직무대행은 그 사람
	ToUnit     int64 // 입사·전보·겸직 조직
	Rank       int64
	Email      string // 입사(새 사람)
	EmployeeNo string
	Role       contract.Role
	Phone      string
	Extension  string
	LastDay    contract.Day // 퇴사: 마지막 근무일
	EndDay     contract.Day // 직무대행 종료일(포함)
	LeaveKind  string
	// 휴직(B50): 구분·예정 복직일(선택)·사유(선택, 민감 정보). 복직은 대상 사람과 효력일만 있으면 된다.
	AbsenceKind   string
	ReturnDay     contract.Day
	AbsenceReason string

	// 코드·사번·직위 이름으로 참조하는 칸(CSV 일괄 반영용). 같은 묶음 안에서 앞서 만들어지는 조직·사람도 가리킬 수 있어
	// applyCommands 가 명령을 처리하기 직전에 번호로 바꾼다(resolveRefs). 번호 칸이 이미 있으면 번호가 우선한다.
	UnitCode, ParentCode, ToUnitCode, UserEmp, RankName string

	// ImportBatch·Source 는 CSV 일괄 반영의 출처 표시로 이벤트 내용에 남는다.
	ImportBatch int64
	Source      string
}

// Outcome 은 등록 결과다.
type Outcome struct {
	EventIDs      []int64
	UnitID        int64 // 신설된 조직 또는 대상 조직
	UserID        int64 // 입사로 만들어졌거나 대상인 사람
	Retroactive   bool
	Warnings      []string // 오류는 아니지만 알려야 할 것(예: 직원 역할의 조직장)
	GainedRisk    []string // 이 발령으로 대상자에게 새로 생긴 위험 권한(role:manage 보유자가 반영한 경우, 감사 상세에 남는다)
	EffectiveDate contract.Day
}

var unitCodeRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,40}$`)

var leaveKinds = map[string]bool{"자진": true, "권고": true, "정년": true, "계약 만료": true, "기타": true}

// LeaveKinds 는 퇴사 사유 구분 목록이다.
func LeaveKinds() []string { return []string{"자진", "권고", "정년", "계약 만료", "기타"} }

// normalize 는 날짜를 채우고 형식·범위·소급 규칙을 검사한다. today 는 지금의 KST 날짜다.
func (c *Command) normalize(today contract.Day) (retro bool, err error) {
	if c.Kind == KindLeave {
		if c.LastDay == "" {
			return false, verr("퇴사일(마지막 근무일)을 입력하세요.")
		}
		d, perr := ParseDay(string(c.LastDay))
		if perr != nil {
			return false, verr(perr.Error())
		}
		c.LastDay = d
		// 효력일은 마지막 근무일 다음 날로 정해진다. 따로 보낸 효력일이 어긋나면 조용히 덮어쓰지 않고 거부한다.
		if c.Effective != "" {
			if e, eerr := ParseDay(string(c.Effective)); eerr != nil || e != AddDays(d, 1) {
				return false, verr(fmt.Sprintf("퇴사 효력일은 마지막 근무일 다음 날(%s)입니다. 퇴사는 마지막 근무일만 입력하세요.", DayText(AddDays(d, 1))))
			}
		}
		c.Effective = AddDays(d, 1)
	} else {
		if c.Effective == "" {
			c.Effective = today
		}
		d, perr := ParseDay(string(c.Effective))
		if perr != nil {
			return false, verr(perr.Error())
		}
		c.Effective = d
	}
	if c.Kind == KindActingSet {
		if c.EndDay == "" {
			return false, verr("직무대행 종료일을 입력하세요.")
		}
		d, perr := ParseDay(string(c.EndDay))
		if perr != nil {
			return false, verr(perr.Error())
		}
		c.EndDay = d
	}
	if c.Kind == KindAbsenceStart && c.ReturnDay != "" {
		d, perr := ParseDay(string(c.ReturnDay))
		if perr != nil {
			return false, verr("예정 복직일: " + perr.Error())
		}
		if d <= c.Effective {
			return false, verr("예정 복직일은 휴직 시작일 이후여야 합니다.")
		}
		c.ReturnDay = d
	}
	if days := DaysBetween(today, c.Effective); days > MaxFutureDays {
		return false, verr("오늘부터 1년 이내 날짜만 예약할 수 있습니다.")
	}
	// 퇴사는 효력일이 마지막 근무일 다음 날이라, 마지막 근무일이 오늘보다 이전이면(이미 지난 퇴사) 소급이다.
	ref := c.Effective
	if c.Kind == KindLeave {
		ref = c.LastDay
	}
	if ref < today {
		retro = true
		if strings.TrimSpace(c.Reason) == "" {
			return true, verr("과거 날짜로 등록하려면 사유를 입력해야 합니다.")
		}
		if days := DaysBetween(ref, today); days > RetroConfirmDays && !c.Confirm {
			return true, &ConfirmError{Days: days, Msg: fmt.Sprintf("%d일 전으로 소급합니다. 이미 지난 날짜입니다. 계속할까요?", days)}
		}
	}
	return retro, nil
}

// event 는 검증을 마친 명령을 이벤트로 바꾼다. 번호를 정해야 하는 값(새 조직·새 사람)은 호출자가 채워서 넘긴다.
func (c Command) event(subjectID int64) Event {
	e := Event{SubjectID: subjectID, Kind: c.Kind, Effective: c.Effective, Reason: strings.TrimSpace(c.Reason)}
	switch c.Kind {
	case KindUnitCreate:
		e.Subject, e.P = SubjectUnit, Payload{Code: c.Code, Name: strings.TrimSpace(c.Name), UnitKind: c.UnitKind, Parent: c.ParentID, Sort: c.Sort}
	case KindUnitRename:
		e.Subject, e.P = SubjectUnit, Payload{Name: strings.TrimSpace(c.Name)}
	case KindUnitMove:
		e.Subject, e.P = SubjectUnit, Payload{Parent: c.ParentID}
	case KindUnitSort:
		e.Subject, e.P = SubjectUnit, Payload{Sort: c.Sort}
	case KindUnitClose, KindUnitReopen, KindHeadClear:
		e.Subject = SubjectUnit
	case KindHeadSet:
		e.Subject, e.P = SubjectUnit, Payload{User: c.UserID}
	case KindActingSet:
		e.Subject, e.P = SubjectUnit, Payload{User: c.UserID, End: c.EndDay}
	case KindHire:
		e.Subject, e.P = SubjectPerson, Payload{Rank: c.Rank, Unit: c.ToUnit}
	case KindTransfer, KindConcurrentAdd, KindConcurrentEnd:
		e.Subject, e.P = SubjectPerson, Payload{Unit: c.ToUnit}
	case KindRankChange:
		e.Subject, e.P = SubjectPerson, Payload{Rank: c.Rank}
	case KindLeave:
		e.Subject, e.P = SubjectPerson, Payload{LastDay: c.LastDay, Leave: c.LeaveKind}
	case KindAbsenceStart:
		e.Subject, e.P = SubjectPerson, Payload{Absence: c.AbsenceKind, ReturnDay: c.ReturnDay, AbsenceReason: strings.TrimSpace(c.AbsenceReason)}
	case KindAbsenceEnd:
		e.Subject = SubjectPerson
	}
	e.P.ImportBatch, e.P.Source = c.ImportBatch, c.Source
	return e
}

func isUnitKind(kind string) bool {
	switch kind {
	case KindUnitCreate, KindUnitRename, KindUnitMove, KindUnitSort, KindUnitClose, KindUnitReopen, KindHeadSet, KindHeadClear, KindActingSet:
		return true
	}
	return false
}

// validate 는 필드 형식과 DB 로만 알 수 있는 값(직위 사용 여부, 이메일·사번 중복, 역할)을 검사한다.
// 규칙 위반(폐지된 조직으로 발령 등)은 Fold 가 맡는다.
func (s *Service) validate(ctx context.Context, q Querier, c *Command) error {
	switch c.Kind {
	case KindUnitCreate:
		if !unitCodeRe.MatchString(c.Code) {
			return verr("조직 코드는 영문·숫자·-만 쓸 수 있습니다(40자 이하).")
		}
		if strings.TrimSpace(c.Name) == "" {
			return verr("조직 이름을 입력하세요.")
		}
		if p := cellProblem(c.Name); p != "" {
			return verr("조직 이름: " + p)
		}
		if !c.UnitKind.IsKnown() {
			return verr("조직 종류를 고르세요.")
		}
	case KindUnitRename:
		if strings.TrimSpace(c.Name) == "" {
			return verr("조직 이름을 입력하세요.")
		}
		if p := cellProblem(c.Name); p != "" {
			return verr("조직 이름: " + p)
		}
	case KindHeadSet, KindActingSet:
		if c.UnitID == 0 || c.UserID == 0 {
			return verr("조직과 사람을 고르세요.")
		}
	case KindHire:
		if c.Rank == 0 || c.ToUnit == 0 {
			return verr("직위와 소속 조직을 고르세요.")
		}
		if c.UserID == 0 {
			if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Email) == "" {
				return verr("이름과 이메일을 입력하세요.")
			}
			if _, err := mail.ParseAddress(strings.TrimSpace(c.Email)); err != nil {
				return verr("이메일 형식이 올바르지 않습니다.")
			}
			var n int
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(email) = lower(?)`, strings.TrimSpace(c.Email)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return verr("이미 사용 중인 이메일입니다.")
			}
			if c.Role == "" {
				c.Role = contract.RoleStaff
			}
			// 기본 템플릿 키 또는 역할 관리에서 만든 사용자 정의 역할 키(B33)여야 한다.
			var rn int
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE key = ? AND deleted_at IS NULL`, string(c.Role)).Scan(&rn); err != nil {
				return err
			}
			if rn == 0 {
				return verr("권한 역할이 올바르지 않습니다.")
			}
			// 위험 권한이 든 역할은 입사 발령으로 줄 수 없다(역할 관리 화면의 2인 확인으로만). 사용자 수정·조직 가져오기와 같은 규칙이다.
			if !c.trustedRole {
				if err := access.CheckAssignable(ctx, q, string(c.Role)); err != nil {
					return err
				}
			}
		}
		if e := strings.TrimSpace(c.EmployeeNo); e != "" {
			var n int
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_people WHERE employee_no = ? AND user_id <> ?`, e, c.UserID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return verr("이미 사용 중인 사번입니다.")
			}
		}
	case KindTransfer, KindConcurrentAdd, KindConcurrentEnd:
		if c.UserID == 0 || c.ToUnit == 0 {
			return verr("사람과 조직을 고르세요.")
		}
	case KindRankChange:
		if c.UserID == 0 || c.Rank == 0 {
			return verr("사람과 직위를 고르세요.")
		}
	case KindLeave:
		if c.UserID == 0 {
			return verr("퇴사할 사람을 고르세요.")
		}
		if c.LeaveKind == "" {
			c.LeaveKind = "기타"
		}
		if !leaveKinds[c.LeaveKind] {
			return verr("퇴사 구분이 올바르지 않습니다.")
		}
	case KindAbsenceStart:
		if c.UserID == 0 {
			return verr("휴직할 사람을 고르세요.")
		}
		if !KnownAbsenceKind(c.AbsenceKind) {
			return verr("휴직 구분을 고르세요.")
		}
		if absenceReasonTooLong(strings.TrimSpace(c.AbsenceReason)) {
			return verr(fmt.Sprintf("휴직 사유는 %d자 이내로 입력하세요.", MaxAbsenceReasonRunes))
		}
	case KindAbsenceEnd:
		if c.UserID == 0 {
			return verr("복직할 사람을 고르세요.")
		}
	case KindUnitMove:
		if c.UnitID == 0 || c.ParentID == 0 {
			return verr("옮길 조직과 새 상위 조직을 고르세요.")
		}
		if err := s.checkEntityMove(ctx, q, c.UnitID, c.ParentID); err != nil {
			return err
		}
	}
	if (c.Kind == KindHire || c.Kind == KindRankChange) && c.Rank != 0 {
		var active int
		err := q.QueryRowContext(ctx, `SELECT active FROM org_ranks WHERE id = ?`, c.Rank).Scan(&active)
		if err != nil {
			return verr("직위를 찾을 수 없습니다.")
		}
		if active != 1 {
			return verr("사용 중지된 직위입니다.")
		}
	}
	if isUnitKind(c.Kind) && c.Kind != KindUnitCreate && c.UnitID == 0 {
		return verr("조직을 고르세요.")
	}
	if !isUnitKind(c.Kind) && c.Kind != KindHire && c.UserID == 0 {
		return verr("사람을 고르세요.")
	}
	return nil
}

// checkEntityMove 는 조직 이동이 조직의 법인을 바꾸는 경우(새 상위가 다른 법인의 조직도에 속함)를 걸러낸다(B44 AC-4416).
// 그 조직을 부서로 가진 업무 데이터(예산)가 있으면 거부한다: 데이터의 법인이 조직을 따라가지 않기 때문이다.
func (s *Service) checkEntityMove(ctx context.Context, q Querier, unitID, parentID int64) error {
	var from, to int64
	if err := q.QueryRowContext(ctx, `SELECT entity_id FROM org_unit_spans WHERE unit_id = ? AND valid_to IS NULL`, unitID).Scan(&from); err != nil {
		return nil // 법인 열이 없는 이전 스키마이거나 구간이 없으면 법인 판단을 하지 않는다
	}
	if err := q.QueryRowContext(ctx, `SELECT entity_id FROM org_unit_spans WHERE unit_id = ? AND valid_to IS NULL`, parentID).Scan(&to); err != nil || from == to {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE department_id = ? AND entity_id > 0`, unitID).Scan(&n); err != nil {
		return err
	}
	if n == 0 { // 구매요청·결재 문서(진행 중·완료 모두)도 그 조직 소유면 같다(B44 AC-4416 2단계 확장)
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_lines l JOIN purchase_requests p ON p.id = l.pr_id WHERE l.requester_unit_id = ? AND p.entity_id > 0`, unitID).Scan(&n); err != nil {
			return err
		}
	}
	if n > 0 {
		return verr("업무 데이터가 있는 조직은 다른 법인으로 옮길 수 없습니다. 새 조직을 만들어 사람과 업무를 옮기세요.")
	}
	return nil
}
