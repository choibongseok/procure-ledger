package org

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/access"
	"regexp"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// mirrored 는 호환 계층의 departments 에 한 줄을 두는 조직 종류다(11.2).
func mirrored(k contract.OrgKind) bool {
	return k == contract.OrgCompany || k == contract.OrgDivision || k == contract.OrgDepartment
}

// Preview 는 등록하기 전에 보여 주는 영향이다. 저장하지 않고 감사 기록도 남기지 않는다.
type Preview struct {
	Effects          []string
	Warnings         []string
	PendingApprovals int         // 퇴사·전보로 결재 자격이 바뀌는 사람이 결재할 차례인 진행 중 건 수
	PendingDocs      []ImpactDoc // 그 건들 중 최대 20건(문서 제목·번호·기안자). 건수가 20을 넘어도 합계는 PendingApprovals
	Retroactive      bool
}

var errPreviewDone = errors.New("미리보기 끝")

// Register 는 명령 한 건을 등록한다. org:manage 권한이 필요하다. 감사 기록은 같은 트랜잭션이다.
func (s *Service) Register(ctx context.Context, p contract.Principal, c Command) (Outcome, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return Outcome{}, err
	}
	var out Outcome
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		outs, _, err := s.applyCommands(ctx, tx, p, []Command{c}, true, nil, nil)
		if err != nil {
			return err
		}
		out = outs[0]
		return nil
	})
	s.invalidate()
	s.denyRiskAttempt(ctx, p, auditActions[c.Kind], err)
	return out, err
}

// RegisterBatch 는 tx 안에서 명령 여러 건을 순서대로 등록한다(시드·일괄 반영용). 권한 검사는 호출자가 한다.
// 이벤트를 모두 추가한 뒤 Fold 를 한 번만 돌려 검증하므로 뒤 명령이 앞 명령의 결과를 본다.
// auditEach 가 false 이면 감사 기록은 호출자가 한 건으로 남긴다.
func (s *Service) RegisterBatch(ctx context.Context, tx contract.Tx, actor contract.Principal, cmds []Command, auditEach bool) ([]Outcome, error) {
	outs, _, err := s.applyCommands(ctx, tx, actor, cmds, auditEach, nil, nil)
	s.invalidate()
	return outs, err
}

// Preview 는 명령을 가상으로 등록해 영향을 계산한다. 아무것도 저장하지 않는다(롤백).
func (s *Service) Preview(ctx context.Context, p contract.Principal, c Command) (Preview, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return Preview{}, err
	}
	var pv Preview
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		_, _, err := s.applyCommands(ctx, tx, p, []Command{c}, false, nil, &pv)
		if err != nil {
			return err
		}
		return errPreviewDone // 롤백
	})
	s.invalidate()
	if errors.Is(err, errPreviewDone) {
		return pv, nil
	}
	return pv, err
}

// Cancel 은 이벤트 하나를 취소한다(취소 이벤트를 추가). 사유가 필수다.
func (s *Service) Cancel(ctx context.Context, p contract.Principal, eventID int64, reason string, confirm bool) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return err
	}
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		_, err := s.cancelTx(ctx, tx, p, eventID, reason, confirm)
		return err
	})
	s.invalidate()
	s.denyRiskAttempt(ctx, p, "hr.event_cancel", err)
	return err
}

// Correct 는 취소와 새 명령 등록을 한 트랜잭션으로 한다(정정). 감사 기록은 한 건이다.
func (s *Service) Correct(ctx context.Context, p contract.Principal, eventID int64, c Command) (Outcome, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return Outcome{}, err
	}
	var out Outcome
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		orig, err := s.loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if (isUnitKind(c.Kind) && orig.Subject != SubjectUnit) || (!isUnitKind(c.Kind) && orig.Subject != SubjectPerson) {
			return verr("정정할 발령과 같은 종류의 대상이어야 합니다.")
		}
		outs, _, err := s.applyCommands(ctx, tx, p, []Command{c}, false, &orig, nil)
		if err != nil {
			return err
		}
		out = outs[0]
		return s.recordCorrect(ctx, tx, p, orig, c, out)
	})
	s.invalidate()
	s.denyRiskAttempt(ctx, p, "hr.event_correct", err)
	return out, err
}

var userTokenRe = regexp.MustCompile(`\{u:(\d+)\}`)

// foldErr 는 규칙 위반 오류의 {u:번호} 자리표시를 사람 이름으로 바꾼 업무 문구 오류로 만든다.
func (s *Service) foldErr(ctx context.Context, q Querier, err error) error {
	var fe *FoldError
	if errors.As(err, &fe) && userTokenRe.MatchString(fe.Msg) {
		cp := *fe
		cp.Msg = userTokenRe.ReplaceAllStringFunc(fe.Msg, func(m string) string {
			id, _ := strconv.ParseInt(userTokenRe.FindStringSubmatch(m)[1], 10, 64)
			return s.userName(ctx, q, id)
		})
		return foldToUserError(&cp)
	}
	return foldToUserError(err)
}

func foldToUserError(err error) error {
	var fe *FoldError
	if errors.As(err, &fe) {
		return &foldUserError{userError{fe.Error()}, fe}
	}
	return err
}

// foldUserError 는 규칙 위반 이벤트를 함께 들고 있는 업무 문구 오류다(CSV 반영이 행 번호로 바꿀 때 쓴다).
type foldUserError struct {
	userError
	Fold *FoldError
}

func (f *foldUserError) Unwrap() error { return f.Fold }

func (s *Service) loadEvent(ctx context.Context, q Querier, id int64) (Event, error) {
	evs, err := LoadEvents(ctx, q)
	if err != nil {
		return Event{}, err
	}
	for _, e := range evs {
		if e.ID == id {
			return e, nil
		}
	}
	return Event{}, contract.ErrNotFound
}

func (s *Service) appendCancel(ctx context.Context, tx contract.Tx, p contract.Principal, target Event, reason string) (int64, error) {
	if strings.TrimSpace(reason) == "" {
		return 0, verr("취소 사유를 입력하세요.")
	}
	if target.Kind == KindCancel {
		return 0, verr("취소 발령은 다시 취소할 수 없습니다(새로 등록하세요).")
	}
	evs, err := LoadEvents(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, e := range evs {
		if e.Kind == KindCancel && e.Cancels == target.ID {
			return 0, verr("이미 취소된 발령입니다.")
		}
	}
	return Append(ctx, tx, Event{Subject: target.Subject, SubjectID: target.SubjectID, Kind: KindCancel, Effective: target.Effective,
		Reason: strings.TrimSpace(reason), Cancels: target.ID, CreatedBy: p.UserID})
}

func (s *Service) cancelTx(ctx context.Context, tx contract.Tx, p contract.Principal, eventID int64, reason string, confirm bool) (Event, error) {
	target, err := s.loadEvent(ctx, tx, eventID)
	if err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Event{}, verr("취소 사유를 입력하세요.")
	}
	today := s.Today()
	if target.Effective < today {
		if days := DaysBetween(target.Effective, today); days > RetroConfirmDays && !confirm {
			return Event{}, &ConfirmError{Days: days, Msg: fmt.Sprintf("%d일 전 발령을 취소합니다. 이미 지난 날짜입니다. 계속할까요?", days)}
		}
	}
	before, err := s.foldAll(ctx, tx)
	if err != nil {
		return Event{}, err
	}
	// 취소로 위험 권한이 되살아나는지(예: 퇴사 발령 취소)도 같은 규칙으로 판정한다.
	var rst *riskState
	if p.UserID != 0 {
		if rst, err = s.riskBefore(ctx, tx, []Command{{Kind: KindTransfer, Effective: target.Effective}}, today); err != nil {
			return Event{}, err
		}
	}
	if _, err := s.appendCancel(ctx, tx, p, target, reason); err != nil {
		return Event{}, err
	}
	res, err := Rebuild(ctx, tx)
	if err != nil {
		if fe := s.foldErr(ctx, tx, err); fe != err {
			return Event{}, &cancelConflict{fe}
		}
		return Event{}, err
	}
	if err := s.guardLastAdmin(ctx, tx, res, today); err != nil {
		return Event{}, err
	}
	if err := s.afterRebuild(ctx, tx, before, res, today); err != nil {
		return Event{}, err
	}
	gained, err := s.riskAfter(ctx, tx, rst, today)
	if err != nil {
		return Event{}, err
	}
	if err := s.applyRiskGain(ctx, tx, p, gained, nil, nil); err != nil {
		return Event{}, err
	}
	if isAbsenceKind(target.Kind) {
		if err := s.runAbsenceHook(ctx, tx, p, target.SubjectID); err != nil {
			return Event{}, err
		}
	}
	return target, s.recordCancel(ctx, tx, p, target, reason, res)
}

// cancelConflict 는 취소가 뒤 이벤트와 맞지 않을 때의 문구를 붙인다.
type cancelConflict struct{ error }

func (c *cancelConflict) Error() string {
	return "취소하지 못했습니다. 이후 발령과 맞지 않습니다(" + c.error.Error() + ")"
}
func (c *cancelConflict) Is(target error) bool { return target == contract.ErrValidation }
func (c *cancelConflict) Unwrap() error        { return c.error }

func (s *Service) foldAll(ctx context.Context, q Querier) (*Result, error) {
	evs, err := LoadEvents(ctx, q)
	if err != nil {
		return nil, err
	}
	return Fold(evs)
}

// unitKindOf 는 조직의 종류다(종류는 바뀌지 않는다).
func unitKindOf(r *Result, id int64) (contract.OrgKind, bool) {
	for _, u := range r.Units {
		if u.UnitID == id {
			return u.Kind, true
		}
	}
	return "", false
}

// applyCommands 는 명령들을 이벤트로 바꿔 추가하고 구간을 다시 쓰고 호환 투영과 감사를 처리한다.
// correcting 이 있으면 그 이벤트를 먼저 취소하고 첫 이벤트가 그것을 정정하는 것으로 기록한다. pv 가 있으면 영향을 채운다.
func (s *Service) applyCommands(ctx context.Context, tx contract.Tx, actor contract.Principal, cmds []Command,
	auditEach bool, correcting *Event, pv *Preview) ([]Outcome, *Result, error) {

	today := s.Today()
	before, err := s.foldAll(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	// 발령 결과로 위험 권한이 새로 생기는지 가늠하려고 적용 전의 위험 권한을 날짜별로 읽어 둔다(시스템 발령자는 제외, risk_gain.go).
	var rst *riskState
	if actor.UserID != 0 || pv != nil {
		if rst, err = s.riskBefore(ctx, tx, cmds, today); err != nil {
			return nil, nil, err
		}
	}
	outs := make([]Outcome, len(cmds))
	if correcting != nil {
		reason := ""
		if len(cmds) > 0 {
			reason = cmds[0].Reason
		}
		if _, err := s.appendCancel(ctx, tx, actor, *correcting, reason); err != nil {
			return nil, nil, err
		}
	}
	for i := range cmds {
		c := &cmds[i]
		if err := s.resolveRefs(ctx, tx, c); err != nil {
			return nil, nil, err
		}
		retro, err := c.normalize(today)
		if err != nil {
			return nil, nil, err
		}
		if err := s.validate(ctx, tx, c); err != nil {
			return nil, nil, err
		}
		out := &outs[i]
		out.Retroactive, out.EffectiveDate = retro, c.Effective
		var subject int64
		switch c.Kind {
		case KindUnitCreate:
			id, err := NewUnitID(ctx, tx, c.Code, c.FixedUnitID)
			if err != nil {
				if dbutil.IsUnique(err) {
					return nil, nil, verr("이미 사용 중인 조직 코드입니다.")
				}
				return nil, nil, err
			}
			subject, out.UnitID = id, id
			if mirrored(c.UnitKind) {
				var existing string
				err := tx.QueryRowContext(ctx, `SELECT name FROM departments WHERE id = ?`, id).Scan(&existing)
				switch {
				case err == nil && existing == strings.TrimSpace(c.Name):
					// 기존 부서 번호를 이어 쓰는 경우: 호환 계층 행이 이미 있다.
				case err == nil:
					return nil, nil, verr("기존 부서 번호의 이름이 다릅니다.")
				case err == sql.ErrNoRows:
					if _, err := tx.ExecContext(ctx, `INSERT INTO departments (id, name) VALUES (?, ?)`, id, strings.TrimSpace(c.Name)); err != nil {
						return nil, nil, verr("본부·부서 이름은 회사 안에서 하나뿐이어야 합니다.")
					}
					if err := dbutil.SyncSequence(ctx, tx, "departments"); err != nil {
						return nil, nil, err
					}
				default:
					return nil, nil, err
				}
			}
		case KindUnitRename:
			subject, out.UnitID = c.UnitID, c.UnitID
			if k, ok := unitKindOf(before, c.UnitID); ok && mirrored(k) {
				if _, err := tx.ExecContext(ctx, `UPDATE departments SET name = ? WHERE id = ?`, strings.TrimSpace(c.Name), c.UnitID); err != nil {
					return nil, nil, verr("본부·부서 이름은 회사 안에서 하나뿐이어야 합니다.")
				}
			}
		case KindHire:
			uid := c.UserID
			if uid == 0 {
				uid, err = s.createUser(ctx, tx, c)
				if err != nil {
					return nil, nil, err
				}
			} else if err := s.touchPerson(ctx, tx, uid, c); err != nil {
				return nil, nil, err
			}
			subject, out.UserID = uid, uid
		default:
			if isUnitKind(c.Kind) {
				subject, out.UnitID = c.UnitID, c.UnitID
				if c.Kind == KindHeadSet || c.Kind == KindActingSet {
					out.UserID = c.UserID
				}
			} else {
				subject, out.UserID = c.UserID, c.UserID
			}
		}
		ev := c.event(subject)
		ev.CreatedBy = actor.UserID
		if correcting != nil && i == 0 {
			ev.Corrects = correcting.ID
		}
		id, err := Append(ctx, tx, ev)
		if err != nil {
			return nil, nil, err
		}
		out.EventIDs = append(out.EventIDs, id)
	}
	res, err := Rebuild(ctx, tx)
	if err != nil {
		return nil, nil, s.foldErr(ctx, tx, err)
	}
	for i, c := range cmds {
		if c.Kind == KindLeave {
			day := c.Effective
			if day < today {
				day = today
			}
			if err := s.guardLastAdmin(ctx, tx, res, day); err != nil {
				return nil, nil, err
			}
		}
		outs[i].Warnings = append(outs[i].Warnings, s.absenceWarnings(ctx, tx, res, c)...)
		if c.Kind == KindHeadSet || c.Kind == KindActingSet {
			if ok, names := decideInfo(ctx, tx, c.UserID); !ok {
				outs[i].Warnings = append(outs[i].Warnings, fmt.Sprintf("이 사람의 권한 역할이 '%s'입니다. 결재하려면 결재 권한이 있는 역할(예: 결재자)을 부여하세요.", names))
			}
		}
	}
	if err := s.afterRebuild(ctx, tx, before, res, today); err != nil {
		return nil, nil, err
	}
	gained, err := s.riskAfter(ctx, tx, rst, today)
	if err != nil {
		return nil, nil, err
	}
	if err := s.applyRiskGain(ctx, tx, actor, gained, outs, pv); err != nil {
		return nil, nil, err
	}
	if pv != nil {
		s.fillPreview(ctx, tx, pv, cmds, outs, before, res)
		return outs, res, nil
	}
	var absent []int64
	if correcting != nil && isAbsenceKind(correcting.Kind) {
		absent = append(absent, correcting.SubjectID)
	}
	for i, c := range cmds {
		if err := s.notifyAbsence(ctx, tx, c, res, outs[i].UserID); err != nil {
			return nil, nil, err
		}
		if isAbsenceKind(c.Kind) {
			absent = append(absent, outs[i].UserID)
		}
	}
	if err := s.runAbsenceHook(ctx, tx, actor, absent...); err != nil {
		return nil, nil, err
	}
	if auditEach {
		for i, c := range cmds {
			if err := s.recordCommand(ctx, tx, actor, c, outs[i], res); err != nil {
				return nil, nil, err
			}
		}
	}
	return outs, res, nil
}

// createUser 는 입사로 새 사용자 계정과 인사 정보(사번·연락처)를 만든다. 부서는 투영이 곧바로 바로잡는 임시 값이다.
func (s *Service) createUser(ctx context.Context, tx contract.Tx, c *Command) (int64, error) {
	var dept int64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(id) FROM departments`).Scan(&dept); err != nil || dept == 0 {
		return 0, verr("조직이 아직 없습니다. 먼저 조직을 만드세요.")
	}
	uid, err := dbutil.InsertID(ctx, tx, `INSERT INTO users (email, name, role, department_id, active) VALUES (?, ?, ?, ?, 1)`,
		strings.TrimSpace(c.Email), strings.TrimSpace(c.Name), string(c.Role), dept)
	if err != nil {
		if dbutil.IsUnique(err) {
			return 0, verr("이미 사용 중인 이메일입니다.")
		}
		return 0, err
	}
	// 사용자 정의 역할로 입사시킨 경우(조직 가져오기 등)에도 그 역할의 직접 부여가 있게 한다(기본 템플릿은 users 트리거가 만든다).
	if err := access.EnsureCompatGrant(ctx, tx, uid, string(c.Role), s.now()); err != nil {
		return 0, err
	}
	no := strings.TrimSpace(c.EmployeeNo)
	if no == "" {
		no = fmt.Sprintf("E%06d", uid)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO org_people (user_id, employee_no, phone, extension) VALUES (?,?,?,?)`,
		uid, no, strings.TrimSpace(c.Phone), strings.TrimSpace(c.Extension)); err != nil {
		if dbutil.IsUnique(err) {
			return 0, verr("이미 사용 중인 사번입니다.")
		}
		return 0, err
	}
	c.EmployeeNo = no
	return uid, nil
}

// touchPerson 은 재입사 때 연락처가 들어오면 갱신한다. 사번은 바꾸지 않는다.
func (s *Service) touchPerson(ctx context.Context, tx contract.Tx, uid int64, c *Command) error {
	var no string
	err := tx.QueryRowContext(ctx, `SELECT employee_no FROM org_people WHERE user_id = ?`, uid).Scan(&no)
	if err == sql.ErrNoRows {
		// 조직에 등록되지 않았던 기존 사용자를 처음 조직에 올린다(예외 E20).
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, uid).Scan(&n); err != nil || n == 0 {
			return contract.ErrNotFound
		}
		no = strings.TrimSpace(c.EmployeeNo)
		if no == "" {
			no = fmt.Sprintf("E%06d", uid)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO org_people (user_id, employee_no, phone, extension) VALUES (?,?,?,?)`,
			uid, no, strings.TrimSpace(c.Phone), strings.TrimSpace(c.Extension)); err != nil {
			if dbutil.IsUnique(err) {
				return verr("이미 사용 중인 사번입니다.")
			}
			return err
		}
	} else if err != nil {
		return err
	}
	c.EmployeeNo = no
	if c.Phone != "" || c.Extension != "" {
		_, err := tx.ExecContext(ctx, `UPDATE org_people SET phone = CASE WHEN ? <> '' THEN ? ELSE phone END,
			extension = CASE WHEN ? <> '' THEN ? ELSE extension END WHERE user_id = ?`, c.Phone, c.Phone, c.Extension, c.Extension, uid)
		return err
	}
	return nil
}

// guardLastAdmin 은 관리자 권한(역할·부여 관리 role:manage, 로그인 설정 auth:manage)을 가진 재직자가 아무도 없어지는 퇴사를 거부한다(예외 E24).
// 보유자는 실효 권한(역할·부여)으로 정한다. 각 권한을 가진 사람이 원래 없는 DB(테스트)는 그 권한을 검사하지 않는다.
func (s *Service) guardLastAdmin(ctx context.Context, tx contract.Tx, res *Result, today contract.Day) error {
	sn := NewSnap(res, today)
	registered := map[int64]bool{}
	for _, p := range res.People {
		registered[p.UserID] = true
	}
	for _, perm := range []contract.Permission{contract.PermRoleManage, contract.PermAuthManage} {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1 WHERE e.permission = ?`, string(perm))
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			continue
		}
		left := true
		for _, id := range ids {
			if !registered[id] || sn.Employed(id) {
				left = false
				break
			}
		}
		if left {
			return verr("관리자 권한을 가진 사람이 없어지게 됩니다.")
		}
	}
	return nil
}

// decideNames 는 사용자의 결재(approval:decide) 권한 보유 여부와 권한을 준 역할 이름들이다.
func decideInfo(ctx context.Context, tx contract.Tx, userID int64) (bool, string) {
	ok, err := access.HasPerm(ctx, tx, userID, contract.PermApprovalDecide)
	if err != nil {
		return true, "" // 알 수 없으면 경고하지 않는다
	}
	names, _ := access.RoleNamesOf(ctx, tx, userID)
	return ok, strings.Join(names, "·")
}

// resolveRefs 는 코드·사번·직위 이름으로 적힌 참조를 번호로 바꾼다(CSV 일괄 반영). 같은 묶음에서 앞서 처리된 신설 조직·입사자도
// 이미 org_units·org_people 에 들어 있으므로 DB 조회로 찾는다. 번호 칸이 이미 채워져 있으면 건드리지 않는다.
func (s *Service) resolveRefs(ctx context.Context, q Querier, c *Command) error {
	unit := func(code string) (int64, error) {
		var id int64
		err := q.QueryRowContext(ctx, `SELECT id FROM org_units WHERE code = ?`, code).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, verr(fmt.Sprintf("없는 조직 코드입니다(\"%s\").", code))
		}
		return id, err
	}
	if c.UnitID == 0 && c.UnitCode != "" {
		id, err := unit(c.UnitCode)
		if err != nil {
			return err
		}
		c.UnitID = id
	}
	if c.ParentID == 0 && c.ParentCode != "" {
		id, err := unit(c.ParentCode)
		if err != nil {
			return err
		}
		c.ParentID = id
	}
	if c.ToUnit == 0 && c.ToUnitCode != "" {
		id, err := unit(c.ToUnitCode)
		if err != nil {
			return err
		}
		c.ToUnit = id
	}
	if c.UserID == 0 && c.UserEmp != "" {
		var id int64
		err := q.QueryRowContext(ctx, `SELECT user_id FROM org_people WHERE employee_no = ?`, c.UserEmp).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return verr(fmt.Sprintf("없는 사번입니다(\"%s\").", c.UserEmp))
		}
		if err != nil {
			return err
		}
		c.UserID = id
	}
	if c.Rank == 0 && c.RankName != "" {
		var id int64
		var active int
		err := q.QueryRowContext(ctx, `SELECT id, active FROM org_ranks WHERE name = ?`, c.RankName).Scan(&id, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return verr(fmt.Sprintf("등록되지 않은 직위입니다(\"%s\").", c.RankName))
		}
		if err != nil {
			return err
		}
		if active == 0 {
			return verr(fmt.Sprintf("사용 중지된 직위입니다(\"%s\").", c.RankName))
		}
		c.Rank = id
	}
	return nil
}
