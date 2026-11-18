package scimtest_test

// B40 2단계(erp): SCIM 요청이 org 발령 경로로 들어갈 때의 통합 시험. 실제 서버 조립(bootstrap.Build)과 라우트·SCIM 토큰 인증을 거친다.
// internal/scim 제품 코드는 platform 소유다. 여기서 드러난 결함은 시험으로 재현한다.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/scim"
	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

type world struct {
	t      *testing.T
	d      deps.Deps
	osvc   *org.Service
	admin  contract.Principal
	c      *scimtest.Client
	t1, t2 int64
}

func scalar(t *testing.T, d deps.Deps, q string, args ...any) string {
	t.Helper()
	var v any
	if err := d.DB.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dbtest.SetEnv(t)
	t.Setenv("PL_DEV_IDP", "")
	t.Setenv("PL_LLM_PROVIDER", "mock")
	cfg := config.Load()
	cfg.BaseURL = "http://localhost:8080"
	d, closeDB, err := bootstrap.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	w := &world{t: t, d: d}
	w.osvc = d.Org.(*org.Service)
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO departments (name) VALUES ('PL 주식회사')`); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := d.DB.QueryRow(`INSERT INTO users (email, name, role, department_id, active) VALUES ('admin@pl.test', '관리자', 'admin', (SELECT MIN(id) FROM departments), 1) RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	w.admin = contract.Principal{UserID: adminID, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession,
		Perms: contract.NewPermSet(contract.PermOrgManage, contract.PermSCIMManage, contract.PermRoleManage)}
	sv := d.SCIM.(*scim.Service)
	co := w.unit("CO", "PL 주식회사", contract.OrgCompany, 0)
	div := w.unit("DIV", "구매본부", contract.OrgDivision, co)
	dep := w.unit("DEP", "구매팀", contract.OrgDepartment, div)
	w.t1 = w.unit("T1", "구매1팀", contract.OrgTeam, dep)
	w.t2 = w.unit("T2", "구매2팀", contract.OrgTeam, dep)
	if _, err := sv.SaveSettings(ctx, w.admin, scim.Settings{Enabled: true, GroupMode: "off", DefaultUnit: "T1", DefaultRank: "사원", DefaultRole: "staff", AdoptExisting: true}); err != nil {
		t.Fatal(err)
	}
	raw, _, err := sv.Issue(ctx, w.admin, "시험", "entra", "users_groups", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rt := contract.NewRouteTable()
	rt.Guard = rbac.NewGuard(d.Audit, d.Txs)
	sv.Register(rt)
	srv := httptest.NewServer(sv.Wrap(rt.Handler()))
	t.Cleanup(srv.Close)
	w.c = &scimtest.Client{T: t, Base: srv.URL, Token: raw}
	return w
}

func (w *world) unit(code, name string, kind contract.OrgKind, parent int64) int64 {
	w.t.Helper()
	o, err := w.osvc.Register(context.Background(), w.admin, org.Command{Kind: org.KindUnitCreate, Code: code, Name: name, UnitKind: kind, ParentID: parent, Effective: "2020-01-01", Reason: "설립", Confirm: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return o.UnitID
}

// addUser 는 화면 경로로 사용자를 만든다(역할 지정이 필요하면 SQL 로 위험 역할을 직접 줄 수 있어 시험이 거부 규칙을 볼 수 있다).
func (w *world) addUser(name, email, role string) int64 {
	w.t.Helper()
	var id int64
	if err := w.d.DB.QueryRow(`INSERT INTO users (email, name, role, department_id, active) VALUES (?, ?, ?, (SELECT MIN(id) FROM departments), 1) RETURNING id`, email, name, role).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.osvc.Register(context.Background(), w.admin, org.Command{Kind: org.KindHire, UserID: id, ToUnit: w.t1, Rank: 1, Effective: "2023-01-02", Reason: "시험", Confirm: true}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) leave(id int64) {
	w.t.Helper()
	if _, err := w.osvc.Register(context.Background(), w.admin, org.Command{Kind: org.KindLeave, UserID: id, LastDay: org.AddDays(w.osvc.Today(), -1), LeaveKind: "기타", Reason: "시험", Confirm: true}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) riskUsers() map[string]bool {
	w.t.Helper()
	out := map[string]bool{}
	rows, err := w.d.DB.Query(`SELECT DISTINCT user_id FROM user_effective_permissions WHERE permission IN ('ledger:write','payment:execute','role:manage','user:manage')`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		out[fmt.Sprint(id)] = true
	}
	return out
}

// 위험 권한을 되살리는 재입사: 퇴사한 재무 담당자를 IdP 가 다시 active=true 로 보내도 거부되고 계정·권한이 되살아나지 않는다. 일반 퇴사자는 재활성된다.
func TestSCIMRehireOfRiskyLeaverIsRejected(t *testing.T) {
	w := newWorld(t)
	fin := w.addUser("재무담당", "fin@pl.test", "finance")
	safe := w.addUser("일반직원", "safe@pl.test", "staff")
	w.leave(fin)
	w.leave(safe)
	risk := w.riskUsers()
	events := scalar(t, w.d, `SELECT COUNT(*) FROM org_events`)
	r := w.c.Patch(fmt.Sprintf("/Users/%d", fin), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
	if r.Status < 400 {
		t.Fatalf("위험 재입사가 받아들여짐: %d %s", r.Status, r.Raw)
	}
	t.Logf("위험 재입사 응답: %d %s", r.Status, r.ScimType())
	if scalar(t, w.d, `SELECT active FROM users WHERE id = ?`, fin) != "0" || scalar(t, w.d, `SELECT COUNT(*) FROM org_events`) != events {
		t.Fatal("거부됐는데 계정이 켜졌거나 이력이 늘었다")
	}
	// 무위험 퇴사자는 PATCH 한 번으로 재활성된다(이력 한 줄, 투영, 감사 채널 scim).
	r = w.c.Patch(fmt.Sprintf("/Users/%d", safe), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
	if r.Status != 200 {
		t.Fatalf("무위험 재입사: %d %s", r.Status, r.Raw)
	}
	if scalar(t, w.d, `SELECT active FROM users WHERE id = ?`, safe) != "1" ||
		scalar(t, w.d, `SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.hire' AND channel = 'scim' AND target_id = ?`, fmt.Sprint(safe)) != "1" {
		t.Fatal("재입사 투영·감사")
	}
	for id := range w.riskUsers() {
		if !risk[id] {
			t.Fatalf("위험 권한자가 늘었다: 사용자 %s", id)
		}
	}
}

// 요청에 날짜 속성이 있어도 효력일은 항상 오늘이다(입사 이력의 효력일 검사).
func TestSCIMIgnoresDatesInRequest(t *testing.T) {
	w := newWorld(t)
	body := scimtest.User("dated@pl.test", "ext-dated", "날짜입사", "T2", "사원")
	body["meta"] = map[string]any{"created": "2019-01-01T00:00:00Z", "lastModified": "2019-01-01T00:00:00Z"}
	body[scimtest.EnterpriseSchema].(map[string]any)["startDate"] = "2019-01-01"
	r := w.c.Post("/Users", body)
	if r.Status != 201 {
		t.Fatalf("입사: %d %s", r.Status, r.Raw)
	}
	today := w.osvc.Today()
	if got := scalar(t, w.d, `SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'hire' AND effective_date = ?`, r.ID(), string(today)); got != "1" {
		t.Fatalf("입사 효력일이 오늘이 아님(%s건 일치)", got)
	}
	if got := scalar(t, w.d, `SELECT department_id FROM users WHERE id = ?`, r.ID()); got == "" {
		t.Fatal("부서 투영")
	}
}

// 불변식: 입사·퇴사·재입사·이름 변경·부서 변경 요청을 섞어 보내도 위험 권한자는 늘지 않고, 이력(구간)과 users.active 투영이 일치한다.
func TestSCIMRequestSequenceKeepsInvariants(t *testing.T) {
	w := newWorld(t)
	fin := w.addUser("재무담당", "fin@pl.test", "finance")
	w.leave(fin)
	risk := w.riskUsers()
	type step struct{ ext, mail, dept string }
	people := []step{{"e1", "a@pl.test", "T1"}, {"e2", "b@pl.test", "T2"}, {"e3", "c@pl.test", "구매2팀"}, {"e4", "d@pl.test", ""}}
	for round := 0; round < 4; round++ {
		for i, p := range people {
			active := (round+i)%2 == 0
			r := w.c.EntraSync(p.ext, p.mail, fmt.Sprintf("직원%d_%d", i, round), p.dept, "사원", active)
			if r.Status >= 500 {
				t.Fatalf("서버 오류: %d %s", r.Status, r.Raw)
			}
		}
		// 위험 권한자를 건드리는 요청도 섞는다: 연결 없는 이메일 채택·재활성 시도.
		w.c.Patch(fmt.Sprintf("/Users/%d", fin), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
		w.c.Delete(fmt.Sprintf("/Users/%d", fin))
		for id := range w.riskUsers() {
			if !risk[id] {
				t.Fatalf("라운드 %d: 위험 권한자가 늘었다: 사용자 %s", round, id)
			}
		}
		rep, err := w.osvc.Consistency(context.Background())
		if err != nil || !rep.OK {
			t.Fatalf("구간 일관성: %+v %v", rep, err)
		}
		if got := scalar(t, w.d, `SELECT COUNT(*) FROM users u JOIN org_people p ON p.user_id = u.id WHERE u.active = 1 AND u.id NOT IN (SELECT user_id FROM org_person_spans WHERE valid_from <= ? AND (valid_to IS NULL OR ? < valid_to) AND left_flag = 0)`,
			string(w.osvc.Today()), string(w.osvc.Today())); got != "0" {
			t.Fatalf("라운드 %d: 재직이 아닌데 계정이 켜진 사람 %s명", round, got)
		}
	}
	if got := scalar(t, w.d, `SELECT COUNT(*) FROM users WHERE email IN ('a@pl.test','b@pl.test','c@pl.test','d@pl.test')`); got == "0" {
		t.Fatal("시험이 아무 사용자도 만들지 못했다")
	}
	if res, err := w.d.AuditReader.Verify(context.Background()); err != nil || !res.ChainOK {
		t.Fatalf("감사 체인: %+v %v", res, err)
	}
}
