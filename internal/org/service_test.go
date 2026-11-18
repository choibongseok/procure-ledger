package org

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t     *testing.T
	db    *sql.DB
	svc   *Service
	store *audit.Store
	now   time.Time
	admin contract.Principal
	ctx   context.Context
}

// 시계는 KST 2026-03-10 정오로 시작한다.
func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(context.Background(), db); err != nil { // 기본 역할 템플릿 권한(B33)
		t.Fatal(err)
	}
	e := &env{t: t, db: db, ctx: context.Background(), now: time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)}
	e.store = audit.New(db)
	// 관리자 계정(조직에 등록되지 않은 기존 사용자)
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (900, '임시')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (900, 'admin@pl.test', '관리자', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"departments", "users"} { // 번호를 직접 넣었으니 PG 시퀀스를 맞춘다(SQLite 는 아무 일도 안 한다)
		if err := dbutil.SyncSequence(context.Background(), db, tbl); err != nil {
			t.Fatal(err)
		}
	}
	e.admin = contract.Principal{UserID: 900, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb}
	svc, err := New(Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: e.store, Masker: rbac.Masker{}, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

// reg 는 성공을 기대한다. 과거 날짜 명령에는 사유와 소급 확인을 자동으로 채운다(소급 규칙 자체를 시험할 때는 svc.Register 를 직접 쓴다).
func (e *env) reg(c Command) Outcome {
	e.t.Helper()
	if c.Effective != "" && c.Effective < e.svc.Today() {
		if c.Reason == "" {
			c.Reason = "테스트"
		}
		c.Confirm = true
	}
	c.trustedRole = true // 시험의 기본 흐름은 위험 권한이 든 역할로도 입사시킨다(거부 규칙은 risky_role_test.go 가 따로 시험한다)
	o, err := e.svc.Register(e.ctx, e.admin, c)
	if err != nil {
		e.t.Fatalf("%s: %v", KindLabel(c.Kind), err)
	}
	return o
}

func (e *env) regErr(c Command) error {
	e.t.Helper()
	_, err := e.svc.Register(e.ctx, e.admin, c)
	if err == nil {
		e.t.Fatalf("%s: 오류를 기대했으나 성공", KindLabel(c.Kind))
	}
	return err
}

func (e *env) auditActions() []string {
	recs, err := e.store.List(e.ctx, caudit.Filter{Limit: 500, Desc: true})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Action)
	}
	return out
}

func (e *env) count(q string, args ...any) int {
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// 기본 조직: 회사 > 구매·물류본부 > 구매팀(부서) > 구매1팀·구매2팀. 반환: 각 조직 번호.
func (e *env) baseOrg() (co, div, dep, t1, t2 int64) {
	co = e.reg(Command{Kind: KindUnitCreate, Code: "CO", Name: "PL 주식회사", UnitKind: contract.OrgCompany, Effective: "2020-01-01", Reason: "설립"}).UnitID
	div = e.reg(Command{Kind: KindUnitCreate, Code: "DIV", Name: "구매·물류본부", UnitKind: contract.OrgDivision, ParentID: co, Effective: "2020-01-01", Reason: "설립"}).UnitID
	dep = e.reg(Command{Kind: KindUnitCreate, Code: "DEP", Name: "구매팀", UnitKind: contract.OrgDepartment, ParentID: div, Effective: "2020-01-01", Reason: "설립"}).UnitID
	t1 = e.reg(Command{Kind: KindUnitCreate, Code: "T1", Name: "구매1팀", UnitKind: contract.OrgTeam, ParentID: dep, Effective: "2020-01-01", Reason: "설립"}).UnitID
	t2 = e.reg(Command{Kind: KindUnitCreate, Code: "T2", Name: "구매2팀", UnitKind: contract.OrgTeam, ParentID: dep, Effective: "2020-01-01", Reason: "설립"}).UnitID
	return
}

func (e *env) hire(name, email string, unit int64, role contract.Role, day string) int64 {
	e.t.Helper()
	return e.reg(Command{Kind: KindHire, Name: name, Email: email, ToUnit: unit, Rank: 1, Role: role, Effective: contract.Day(day), Reason: "입사 등록"}).UserID
}

func userRow(e *env, id int64) (active int, dept int64, pos string) {
	var p sql.NullString
	if err := e.db.QueryRow(`SELECT active, department_id, position FROM users WHERE id = ?`, id).Scan(&active, &dept, &p); err != nil {
		e.t.Fatal(err)
	}
	return active, dept, p.String
}

// 입사·조직장 보임이 호환 투영(부서 번호·직책)과 감사 기록으로 이어진다.
func TestRegisterProjectionAndAudit(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, t2 := e.baseOrg()
	staff := e.hire("구매직원", "staff@pl.test", t1, contract.RoleStaff, "2021-01-04")
	lead := e.hire("구매팀장", "lead@pl.test", t1, contract.RoleManager, "2021-01-04")
	head := e.hire("구매부서장", "head@pl.test", dep, contract.RoleManager, "2021-01-04")
	lead2 := e.hire("구매부팀장", "lead2@pl.test", t2, contract.RoleManager, "2021-01-04")
	o := e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: lead, Effective: "2021-02-01", Reason: "보임"})
	if len(o.Warnings) != 0 {
		t.Fatalf("결재자에게는 경고 없음: %v", o.Warnings)
	}
	e.reg(Command{Kind: KindHeadSet, UnitID: dep, UserID: head, Effective: "2021-02-01"})
	e.reg(Command{Kind: KindHeadSet, UnitID: t2, UserID: lead2, Effective: "2021-02-01"})
	// 직원 역할인 사람을 조직장으로 보임하면 경고만 한다.
	if o := e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: staff, Effective: "2021-03-01"}); len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "'직원'") {
		t.Fatalf("직원 역할 경고: %v", o.Warnings)
	}
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: lead, Effective: "2021-04-01"}) // 되돌림

	for name, want := range map[string]struct {
		id   int64
		dept int64
		pos  string
	}{"직원": {staff, dep, ""}, "팀장": {lead, dep, "team_lead"}, "부서장": {head, dep, "dept_head"}, "부팀장": {lead2, dep, "team_lead"}} {
		active, dept, pos := userRow(e, want.id)
		if active != 1 || dept != want.dept || pos != want.pos {
			t.Errorf("%s 투영: active=%d dept=%d pos=%q, want dept=%d pos=%q", name, active, dept, pos, want.dept, want.pos)
		}
	}
	// 구간 테이블은 이벤트 로그와 일치한다.
	if err := Consistent(e.ctx, e.db); err != nil {
		t.Fatal(err)
	}
	// 감사: 조직 신설 5 + 입사 4 + 조직장 보임 5, 모두 행위자는 관리자(웹)
	acts := e.auditActions()
	counts := map[string]int{}
	for _, a := range acts {
		counts[a]++
	}
	if counts["org.unit_create"] != 5 || counts["hr.hire"] != 4 || counts["org.head_set"] != 5 {
		t.Fatalf("감사 동작 수: %v", counts)
	}
	recs, _ := e.store.List(e.ctx, caudit.Filter{Action: "hr.hire", Limit: 10})
	for _, r := range recs {
		if r.ActorID != 900 || r.Channel != contract.ChannelWeb || r.TargetType != caudit.TargetPerson {
			t.Fatalf("입사 감사 행위자·대상: %+v", r)
		}
		for _, d := range r.Diff {
			if strings.Contains(d.New, "@") {
				t.Fatalf("이메일이 감사 diff 에 들어감: %+v", d)
			}
		}
	}
}

// 권한 없는 역할은 등록·미리보기·취소·정정이 모두 거부된다(AC-66).
func TestOrgManageRequiresPermission(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	for _, role := range []contract.Role{contract.RoleFinance, contract.RoleManager, contract.RoleStaff, contract.RoleAuditor} {
		p := contract.Principal{UserID: 5, Role: role, Channel: contract.ChannelWeb}
		if _, err := e.svc.Register(e.ctx, p, Command{Kind: KindUnitSort, UnitID: t1, Sort: 3}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 등록: %v", role, err)
		}
		if _, err := e.svc.Preview(e.ctx, p, Command{Kind: KindUnitSort, UnitID: t1}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 미리보기: %v", role, err)
		}
		if err := e.svc.Cancel(e.ctx, p, 1, "x", false); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 취소: %v", role, err)
		}
		if _, err := e.svc.Correct(e.ctx, p, 1, Command{Kind: KindUnitSort, UnitID: t1}); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 정정: %v", role, err)
		}
	}
}

// 소급·예약 규칙(AC-32, AC-37)과 입력 검증.
func TestRetroactiveAndFutureRules(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	u := e.hire("직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")

	// 사유 없는 소급
	if err := e.regErr(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-01"}); !strings.Contains(err.Error(), "사유를 입력") {
		t.Fatalf("사유 문구: %v", err)
	}
	// 30일 넘는 소급은 확인 필요
	err := e.regErr(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-01-01", Reason: "소급"})
	var ce *ConfirmError
	if !errors.As(err, &ce) || ce.Days != 68 || !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("확인 오류: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE kind = 'transfer'`) != 0 {
		t.Fatal("확인 전에는 저장되지 않아야 함")
	}
	o := e.reg(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-01-01", Reason: "소급", Confirm: true})
	if !o.Retroactive {
		t.Fatal("소급 표시")
	}
	recs, _ := e.store.List(e.ctx, caudit.Filter{Action: "hr.transfer", Limit: 5})
	foundRetro := false
	for _, d := range recs[0].Diff {
		foundRetro = foundRetro || (d.Key == "retroactive" && d.New == "예")
	}
	if !foundRetro {
		t.Fatalf("감사에 소급 표시: %+v", recs[0].Diff)
	}
	// 30일 이내 소급은 확인 없이(사유만)
	e.reg(Command{Kind: KindTransfer, UserID: u, ToUnit: t1, Effective: "2026-02-20", Reason: "정정"})
	// 입사일보다 이른 소급: Fold 가 거부(입사 전에는 사람이 없음)
	if err := e.regErr(Command{Kind: KindRankChange, UserID: u, Rank: 2, Effective: "2020-06-01", Reason: "x", Confirm: true}); !strings.Contains(err.Error(), "입사 전인 사람에게는 발령할 수 없습니다") {
		t.Fatalf("입사 전 소급: %v", err)
	}
	// 예약은 365일까지
	if err := e.regErr(Command{Kind: KindUnitSort, UnitID: t1, Sort: 9, Effective: "2027-03-12"}); !strings.Contains(err.Error(), "1년 이내") {
		t.Fatalf("예약 한도: %v", err)
	}
	e.reg(Command{Kind: KindUnitSort, UnitID: t1, Sort: 9, Effective: "2027-03-10"})
	// 날짜 형식
	if err := e.regErr(Command{Kind: KindUnitSort, UnitID: t1, Sort: 9, Effective: "2026-02-30"}); !strings.Contains(err.Error(), "연-월-일(예 2026-03-01)") {
		t.Fatalf("날짜 형식: %v", err)
	}
}

func TestHireValidation(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	e.hire("직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	for name, c := range map[string]struct {
		cmd  Command
		want string
	}{
		"이름 누락":  {Command{Kind: KindHire, Email: "x@pl.test", ToUnit: t1, Rank: 1}, "이름과 이메일"},
		"이메일 중복": {Command{Kind: KindHire, Name: "y", Email: "A@pl.test", ToUnit: t1, Rank: 1}, "이미 사용 중인 이메일"},
		"이메일 형식": {Command{Kind: KindHire, Name: "y", Email: "not-an-email", ToUnit: t1, Rank: 1}, "형식"},
		"역할 오류":  {Command{Kind: KindHire, Name: "y", Email: "y@pl.test", ToUnit: t1, Rank: 1, Role: "root"}, "권한 역할"},
		"직위 없음":  {Command{Kind: KindHire, Name: "y", Email: "y@pl.test", ToUnit: t1}, "직위와 소속"},
	} {
		c.cmd.Effective = "2026-03-10"
		if err := e.regErr(c.cmd); !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want %q)", name, err, c.want)
		}
	}
	// 사번 중복
	e.reg(Command{Kind: KindHire, Name: "z", Email: "z@pl.test", ToUnit: t1, Rank: 1, EmployeeNo: "K-100"})
	if err := e.regErr(Command{Kind: KindHire, Name: "w", Email: "w@pl.test", ToUnit: t1, Rank: 1, EmployeeNo: "K-100"}); !strings.Contains(err.Error(), "사번") {
		t.Fatalf("사번 중복: %v", err)
	}
	// 실패한 등록은 사용자 행도 남기지 않는다(롤백).
	if e.count(`SELECT COUNT(*) FROM users WHERE email IN ('w@pl.test','y@pl.test','x@pl.test')`) != 0 {
		t.Fatal("실패한 입사가 사용자를 남김")
	}
	// 사용 중지된 직위
	if _, err := e.db.Exec(`UPDATE org_ranks SET active = 0 WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	if err := e.regErr(Command{Kind: KindHire, Name: "q", Email: "q@pl.test", ToUnit: t1, Rank: 3}); !strings.Contains(err.Error(), "사용 중지") {
		t.Fatalf("중지된 직위: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM org_ranks`) < 5 {
		t.Fatal("직위 시드")
	}
}

// 취소와 정정(AC-36, AC-36a).
func TestCancelAndCorrect(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, t2 := e.baseOrg()
	u := e.hire("직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	tr := e.reg(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-10"})
	_, _, dep0 := func() (int, int64, string) { a, d, p := userRow(e, u); return a, d, p }()
	_ = dep0
	// 사유 없는 취소 거부
	if err := e.svc.Cancel(e.ctx, e.admin, tr.EventIDs[0], "", false); err == nil || !strings.Contains(err.Error(), "취소 사유") {
		t.Fatalf("취소 사유: %v", err)
	}
	if err := e.svc.Cancel(e.ctx, e.admin, tr.EventIDs[0], "잘못 입력", false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(e.ctx, e.admin, tr.EventIDs[0], "또", false); err == nil || !strings.Contains(err.Error(), "이미 취소") {
		t.Fatalf("이중 취소: %v", err)
	}
	sn, _ := e.svc.snap(e.ctx, "2026-03-10")
	if sn.People[u].Primary != t1 {
		t.Fatalf("취소된 전보는 반영되지 않아야 함: %+v", sn.People[u])
	}
	// 정정: 한 번에 감사 1건(hr.event_correct), 이벤트 로그에는 취소 + 새 이벤트
	tr2 := e.reg(Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-03-10"})
	before := len(e.auditActions())
	events0 := e.count(`SELECT COUNT(*) FROM org_events`)
	out, err := e.svc.Correct(e.ctx, e.admin, tr2.EventIDs[0], Command{Kind: KindTransfer, UserID: u, ToUnit: dep, Effective: "2026-03-10", Reason: "조직 착오"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(e.auditActions()) - before; got != 1 {
		t.Fatalf("정정의 감사 기록은 한 건이어야 함: %d", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM org_events`) - events0; got != 2 {
		t.Fatalf("정정은 이벤트 로그에 두 줄: %d", got)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE corrects_id = ?`, tr2.EventIDs[0]) != 1 || len(out.EventIDs) != 1 {
		t.Fatal("정정 이벤트가 원본을 가리켜야 함")
	}
	acts := e.auditActions()
	if acts[0] != "hr.event_correct" {
		t.Fatalf("최신 감사 동작: %v", acts[:3])
	}
	// 정정이 검증에 실패하면 취소도 되지 않는다.
	ev := e.count(`SELECT COUNT(*) FROM org_events`)
	if _, err := e.svc.Correct(e.ctx, e.admin, tr2.EventIDs[0], Command{Kind: KindTransfer, UserID: u, ToUnit: 9999, Effective: "2026-03-10", Reason: "x"}); err == nil {
		t.Fatal("없는 조직으로의 정정은 실패해야 함")
	}
	if e.count(`SELECT COUNT(*) FROM org_events`) != ev {
		t.Fatal("실패한 정정이 이벤트를 남김(롤백되지 않음)")
	}
	// 취소가 뒤 이벤트와 맞지 않으면 문구와 함께 거부: 입사를 취소하려는데 뒤에 전보가 있다.
	hireEv := e.count(`SELECT id FROM org_events WHERE kind = 'hire' LIMIT 1`)
	err = e.svc.Cancel(e.ctx, e.admin, int64(hireEv), "착오", true)
	if err == nil || !strings.Contains(err.Error(), "이후 발령과 맞지 않습니다") {
		t.Fatalf("입사 취소 충돌: %v", err)
	}
}

// 미리보기는 아무것도 저장하지 않고 감사도 남기지 않는다(AC-38).
func TestPreviewStoresNothing(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, t2 := e.baseOrg()
	u := e.hire("구매직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	ev, au, us := e.count(`SELECT COUNT(*) FROM org_events`), e.count(`SELECT COUNT(*) FROM audit_logs`), e.count(`SELECT COUNT(*) FROM users`)
	pv, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindTransfer, UserID: u, ToUnit: t2, Effective: "2026-04-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Effects) == 0 || !strings.Contains(pv.Effects[0], "주소속이") || !strings.Contains(pv.Effects[0], "구매1팀") {
		t.Fatalf("영향 문구: %v", pv.Effects)
	}
	pv2, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindHire, Name: "새사람", Email: "n@pl.test", ToUnit: t1, Rank: 1, Effective: "2026-04-01"})
	if err != nil || len(pv2.Effects) == 0 {
		t.Fatalf("입사 미리보기: %v %v", pv2, err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events`) != ev || e.count(`SELECT COUNT(*) FROM audit_logs`) != au || e.count(`SELECT COUNT(*) FROM users`) != us {
		t.Fatal("미리보기가 저장함")
	}
	// 규칙 위반은 미리보기에서도 같은 문구로 알린다.
	if _, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindUnitClose, UnitID: t1, Effective: "2026-04-01"}); err == nil || !strings.Contains(err.Error(), "소속 인원이 남아") {
		t.Fatalf("폐지 미리보기: %v", err)
	}
}

// 퇴사: 효력일 다음 날부터 계정이 꺼지고 세션·토큰·위임이 정리된다. 행위자는 시스템(AC-50, AC-69b).
func TestLeaveDeactivatesAndCleansUp(t *testing.T) {
	e := newEnv(t)
	_, _, dep, t1, _ := e.baseOrg()
	u := e.hire("구매팀장", "lead@pl.test", t1, contract.RoleManager, "2021-01-04")
	other := e.hire("구매부서장", "head@pl.test", dep, contract.RoleManager, "2021-01-04")
	e.reg(Command{Kind: KindHeadSet, UnitID: t1, UserID: u, Effective: "2021-02-01"})
	now := e.now.UTC().Format(time.RFC3339)
	if _, err := e.db.Exec(`INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at) VALUES ('h1', ?, 'c', ?, '2099-01-01T00:00:00Z', ?)`, u, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO personal_tokens (user_id, name, token_prefix, token_hash, created_at, expires_at) VALUES (?, 't', 'plp_ab', 'hh1', ?, '2099-01-01T00:00:00Z')`, u, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_delegations (delegator_id, delegate_id, start_at, end_at, created_at) VALUES (?, ?, '2026-03-01T00:00:00Z', '2099-01-01T00:00:00Z', ?)`, u, other, now); err != nil {
		t.Fatal(err)
	}
	// 오늘이 마지막 근무일(3/10)이면 아직 재직이다.
	pv, err := e.svc.Preview(e.ctx, e.admin, Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-10", LeaveKind: "자진"})
	if err != nil || len(pv.Effects) < 2 {
		t.Fatalf("퇴사 미리보기: %v %v", pv, err)
	}
	e.reg(Command{Kind: KindLeave, UserID: u, LastDay: "2026-03-10", LeaveKind: "자진"})
	if active, _, _ := userRow(e, u); active != 1 {
		t.Fatal("마지막 근무일에는 계정이 켜져 있어야 함")
	}
	// 시계를 다음 날 00:00 KST(= UTC 15:00)로 넘기고 갱신한다.
	e.now = time.Date(2026, 3, 10, 14, 59, 59, 0, time.UTC)
	if n, err := e.svc.RefreshProjection(e.ctx); err != nil || n != 0 {
		t.Fatalf("마지막 근무일 23:59:59: %d %v", n, err)
	}
	e.now = time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)
	n, err := e.svc.RefreshProjection(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("다음 날 00:00 갱신: %d %v", n, err)
	}
	if active, _, _ := userRow(e, u); active != 0 {
		t.Fatal("퇴사 뒤 계정이 꺼져야 함")
	}
	if e.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, u) != 0 ||
		e.count(`SELECT COUNT(*) FROM personal_tokens WHERE user_id = ? AND revoked_at IS NULL`, u) != 0 ||
		e.count(`SELECT COUNT(*) FROM approval_delegations WHERE delegator_id = ? AND revoked_at IS NULL`, u) != 0 {
		t.Fatal("세션·토큰·위임이 정리되어야 함")
	}
	// 감사: 시스템 행위자(번호 0, 역할 system)로 기존 동작 이름이 남는다.
	for _, act := range []string{"token.revoke", "delegation.revoke", "user.update", "org.projection_apply"} {
		recs, _ := e.store.List(e.ctx, caudit.Filter{Action: act, Limit: 5})
		if len(recs) != 1 || recs[0].ActorID != 0 || recs[0].ActorRole != "system" {
			t.Fatalf("%s 감사 행위자: %+v", act, recs)
		}
	}
	// 멱등: 같은 날 다시 돌려도 바뀌는 것이 없다.
	if n, err := e.svc.RefreshProjection(e.ctx); err != nil || n != 0 {
		t.Fatalf("멱등: %d %v", n, err)
	}
	// 조직장이던 조직은 공석이고 투영된 직책은 사라진다.
	sn, _ := e.svc.snap(e.ctx, "2026-03-11")
	if sn.Units[t1].HeadUser != 0 {
		t.Fatalf("퇴사자의 조직은 공석: %+v", sn.Units[t1])
	}
	// 퇴사자에게 발령하면 거부, 재입사는 계정을 다시 켠다(AC-53).
	if err := e.regErr(Command{Kind: KindTransfer, UserID: u, ToUnit: dep, Effective: "2026-03-12"}); !strings.Contains(err.Error(), "퇴사한 사람") {
		t.Fatalf("퇴사자 발령: %v", err)
	}
	e.reg(Command{Kind: KindHire, UserID: u, ToUnit: t1, Rank: 1, Effective: "2026-03-11"})
	if active, _, _ := userRow(e, u); active != 1 {
		t.Fatal("재입사하면 같은 계정이 다시 켜져야 함")
	}
}

// 관리자가 계정을 직접 끈 사람은 날짜가 바뀌어도 다시 켜지지 않는다.
func TestRefreshDoesNotReviveAdminDisabledUser(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("직원", "a@pl.test", t1, contract.RoleStaff, "2021-01-04")
	if n, err := e.svc.RefreshProjection(e.ctx); err != nil {
		t.Fatalf("첫 실행: %d %v", n, err)
	}
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE id = ?`, u); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.now = e.now.Add(24 * time.Hour)
		if _, err := e.svc.RefreshProjection(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if active, _, _ := userRow(e, u); active != 0 {
		t.Fatal("관리자가 끈 계정이 되살아남")
	}
}

// 예약 입사: 입사일 전에는 계정이 꺼져 있고 입사일에 켜진다.
func TestFutureHireActivatesOnDate(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	u := e.hire("신입", "new@pl.test", t1, contract.RoleStaff, "2026-03-20")
	if active, _, _ := userRow(e, u); active != 0 {
		t.Fatal("입사 전에는 계정이 꺼져 있어야 함")
	}
	e.now = time.Date(2026, 3, 19, 14, 59, 59, 0, time.UTC)
	if _, err := e.svc.RefreshProjection(e.ctx); err != nil {
		t.Fatal(err)
	}
	if active, _, _ := userRow(e, u); active != 0 {
		t.Fatal("입사 전날까지는 꺼짐")
	}
	e.now = time.Date(2026, 3, 19, 15, 0, 0, 0, time.UTC)
	if n, err := e.svc.RefreshProjection(e.ctx); err != nil || n != 1 {
		t.Fatalf("입사일 갱신: %d %v", n, err)
	}
	if active, _, _ := userRow(e, u); active != 1 {
		t.Fatal("입사일에 계정이 켜져야 함")
	}
}

// 마지막 관리자의 퇴사는 거부된다(AC-54).
func TestLastAdminCannotLeave(t *testing.T) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	a := e.hire("관리자2", "admin2@pl.test", t1, contract.RoleAdmin, "2021-01-04")
	// 조직에 등록되지 않은 기존 관리자(900)가 있으므로 퇴사해도 관리자가 남는다.
	e.reg(Command{Kind: KindLeave, UserID: a, LastDay: "2026-03-20", LeaveKind: "자진"})
	// 900 을 조직에 등록하고 둘 다 퇴사시키려 하면 두 번째에서 거부된다.
	if _, err := e.db.Exec(`UPDATE users SET active = 1 WHERE id = 900`); err != nil {
		t.Fatal(err)
	}
	e.reg(Command{Kind: KindHire, UserID: 900, ToUnit: t1, Rank: 1, Effective: "2026-03-10"})
	if err := e.regErr(Command{Kind: KindLeave, UserID: 900, LastDay: "2026-03-25", LeaveKind: "자진"}); !strings.Contains(err.Error(), "관리자 권한을 가진 사람이 없어지게") {
		t.Fatalf("마지막 관리자 퇴사: %v", err)
	}
}

// 조직 변경 규칙은 서비스에서도 같은 문구로 막는다(AC-03~07).
func TestUnitRulesThroughService(t *testing.T) {
	e := newEnv(t)
	co, div, dep, t1, _ := e.baseOrg()
	if err := e.regErr(Command{Kind: KindUnitCreate, Code: "X", Name: "구매팀", UnitKind: contract.OrgDepartment, ParentID: div}); !strings.Contains(err.Error(), "회사 안에서 하나뿐") {
		t.Fatalf("부서 이름 중복: %v", err)
	}
	if err := e.regErr(Command{Kind: KindUnitCreate, Code: "T1", Name: "새 팀", UnitKind: contract.OrgTeam, ParentID: dep}); !strings.Contains(err.Error(), "조직 코드") {
		t.Fatalf("코드 중복: %v", err)
	}
	if err := e.regErr(Command{Kind: KindUnitCreate, Code: "bad code", Name: "새 팀", UnitKind: contract.OrgTeam, ParentID: dep}); !strings.Contains(err.Error(), "영문·숫자") {
		t.Fatalf("코드 형식: %v", err)
	}
	if err := e.regErr(Command{Kind: KindUnitMove, UnitID: div, ParentID: t1}); !strings.Contains(err.Error(), "자기 하위 조직") {
		t.Fatalf("순환: %v", err)
	}
	if err := e.regErr(Command{Kind: KindUnitClose, UnitID: dep}); !strings.Contains(err.Error(), "하위 조직이 남아") {
		t.Fatalf("폐지: %v", err)
	}
	if err := e.regErr(Command{Kind: KindUnitClose, UnitID: co}); !strings.Contains(err.Error(), "회사") {
		t.Fatalf("회사 폐지: %v", err)
	}
	// 부서 이름 변경은 호환 계층(departments)에도 반영된다.
	e.reg(Command{Kind: KindUnitRename, UnitID: dep, Name: "구매관리팀"})
	var name string
	_ = e.db.QueryRow(`SELECT name FROM departments WHERE id = ?`, dep).Scan(&name)
	if name != "구매관리팀" {
		t.Fatalf("departments 이름: %q", name)
	}
	// 팀은 departments 에 들어가지 않는다.
	if e.count(`SELECT COUNT(*) FROM departments WHERE id = ?`, t1) != 0 {
		t.Fatal("팀이 departments 에 들어감")
	}
	// 직무대행: 조직장이 없는 팀에만, 겹침 거부
	u := e.hire("대행자", "d@pl.test", dep, contract.RoleManager, "2021-01-04")
	e.reg(Command{Kind: KindActingSet, UnitID: t1, UserID: u, Effective: "2026-04-01", EndDay: "2026-04-10"})
	if err := e.regErr(Command{Kind: KindActingSet, UnitID: t1, UserID: u, Effective: "2026-04-05", EndDay: "2026-04-20"}); !strings.Contains(err.Error(), "이미 직무대행이 있습니다") {
		t.Fatalf("대행 겹침: %v", err)
	}
	if err := e.regErr(Command{Kind: KindActingSet, UnitID: t1, UserID: u, Effective: "2026-04-11"}); !strings.Contains(err.Error(), "종료일") {
		t.Fatalf("종료일 필수: %v", err)
	}
}

func TestOrgAuditLabelsExist(t *testing.T) {
	for _, a := range auditActions {
		if !strings.HasPrefix(a, "org.") && !strings.HasPrefix(a, "hr.") {
			t.Errorf("조직 감사 동작 이름 규칙: %s", a)
		}
	}
	if len(auditActions) != 17 {
		t.Fatal(fmt.Sprintf("감사 동작 매핑 수 %d", len(auditActions)))
	}
}

// 회사(루트) 신설 이벤트의 취소처럼 뒤 이벤트와 반드시 어긋나는 취소는 자연스러운 문구로 거부된다.
func TestCancelRootCreationRejectedWithMessage(t *testing.T) {
	e := newEnv(t)
	e.baseOrg()
	var rootEv int64
	if err := e.db.QueryRow(`SELECT MIN(id) FROM org_events WHERE kind = 'unit_create'`).Scan(&rootEv); err != nil {
		t.Fatal(err)
	}
	err := e.svc.Cancel(e.ctx, e.admin, rootEv, "착오", true)
	if err == nil || !strings.Contains(err.Error(), "취소하지 못했습니다") && !strings.Contains(err.Error(), "이후 발령과 맞지 않습니다") {
		t.Fatalf("루트 신설 취소: %v", err)
	}
	if !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("400 으로 옮겨져야 함: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE kind = 'cancel'`) != 0 {
		t.Fatal("거부된 취소가 이벤트를 남김")
	}
}
