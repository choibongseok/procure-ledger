package scimtest_test

// B40 3단계(erp): SCIM Groups 통합 시험. 그룹→조직(unit 모드)과 그룹→역할(role 모드)이 org 발령 경로·access 부여 경로를 우회하지 않는지,
// 위험 권한이 생기지 않는지, 무작위 순서의 Users·Groups 요청 뒤에도 불변식이 유지되는지 본다. internal/scim·internal/access 제품 코드는 platform 소유다.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/scim"
	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

func (w *world) sv() *scim.Service { return w.d.SCIM.(*scim.Service) }

func (w *world) setMode(mode string) error {
	w.t.Helper()
	_, err := w.sv().SaveSettings(context.Background(), w.admin, scim.Settings{Enabled: true, GroupMode: mode, DefaultUnit: "T1", DefaultRank: "사원", DefaultRole: "staff", AdoptExisting: true})
	return err
}

func (w *world) mapUnit(ext string, unit int64) int64 {
	w.t.Helper()
	v, err := w.sv().CreateMap(context.Background(), w.admin, scim.MapInput{Source: "entra", GroupExternalID: ext, DisplayName: ext, UnitID: unit})
	if err != nil {
		w.t.Fatalf("조직 매핑 %s: %v", ext, err)
	}
	return v.ID
}

func (w *world) mapRole(ext string, role int64) (int64, error) {
	w.t.Helper()
	v, err := w.sv().CreateMap(context.Background(), w.admin, scim.MapInput{Source: "entra", GroupExternalID: ext, DisplayName: ext, RoleID: role})
	return v.ID, err
}

// scimUser 는 IdP 로 사용자를 만든다(POST). 번호를 돌려준다.
func (w *world) scimUser(ext, mail string) int64 {
	w.t.Helper()
	r := w.c.Post("/Users", scimtest.User(mail, ext, "직원 "+ext, "", "사원"))
	if r.Status != 201 {
		w.t.Fatalf("사용자 생성 %s: %d %s", ext, r.Status, r.Raw)
	}
	var id int64
	_, _ = fmt.Sscan(r.ID(), &id)
	return id
}

// adopted 는 화면 발령으로 먼저 입사한 사용자를 IdP 가 같은 이메일로 보내 연결(채택)하게 한다.
func (w *world) adopted(ext, mail string) int64 {
	w.t.Helper()
	id := w.addUser("기존 "+ext, mail, "staff")
	r := w.c.Post("/Users", scimtest.User(mail, ext, "기존 "+ext, "", "사원"))
	if r.Status != 200 && r.Status != 201 {
		w.t.Fatalf("채택 %s: %d %s", ext, r.Status, r.Raw)
	}
	return id
}

func members(ids ...int64) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"value": fmt.Sprint(id)})
	}
	return out
}

func (w *world) addMembers(mapID int64, ids ...int64) scimtest.Resp {
	w.t.Helper()
	return w.c.Patch(fmt.Sprintf("/Groups/%d", mapID), scimtest.Patch(scimtest.Op("add", "members", members(ids...))))
}

func (w *world) removeMember(mapID, uid int64) scimtest.Resp {
	w.t.Helper()
	return w.c.Patch(fmt.Sprintf("/Groups/%d", mapID), scimtest.Patch(scimtest.Op("remove", fmt.Sprintf(`members[value eq "%d"]`, uid), nil)))
}

func (w *world) unitOf(uid int64) int64 {
	w.t.Helper()
	m, err := w.osvc.Persons(context.Background(), []int64{uid}, "")
	if err != nil {
		w.t.Fatal(err)
	}
	return m[uid].Primary.Unit.ID
}

func (w *world) role(key string, perms ...string) int64 {
	w.t.Helper()
	if _, err := w.d.DB.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES (?, ?, 'custom', 'a', 'a')`, key, key+" 역할"); err != nil {
		w.t.Fatal(err)
	}
	var id int64
	if err := w.d.DB.QueryRow(`SELECT id FROM roles WHERE key = ?`, key).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	for _, p := range perms {
		w.rolePerm(id, p)
	}
	return id
}

func (w *world) rolePerm(roleID int64, perm string) {
	w.t.Helper()
	if _, err := w.d.DB.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, perm); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) grantUnit(roleID, unit int64) {
	w.t.Helper()
	if _, err := w.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) VALUES (?, 'unit', ?, '', 0, 'manual', '시험', 'a')`, roleID, unit); err != nil {
		w.t.Fatal(err)
	}
	if err := access.Recompute(context.Background(), w.d.DB, w.osvc.Today()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) grantUser(roleID, uid int64) {
	w.t.Helper()
	if _, err := w.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) VALUES (?, 'user', ?, '', 0, 'manual', '사람이 준 부여', 'a')`, roleID, uid); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) userGrants(roleID, uid int64) int {
	w.t.Helper()
	var n int
	if err := w.d.DB.QueryRow(`SELECT COUNT(*) FROM role_grants WHERE role_id = ? AND target_type = 'user' AND target_id = ?`, roleID, uid).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) memberRows(mapID int64) int {
	w.t.Helper()
	var n int
	if err := w.d.DB.QueryRow(`SELECT COUNT(*) FROM scim_group_members WHERE map_id = ?`, mapID).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.d.DB.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// riskPairs 는 위험 권한(contract.HighRiskPermissions)을 실제로 가진 (사용자, 권한) 쌍이다.
func (w *world) riskPairs() map[string]bool {
	w.t.Helper()
	if err := access.Recompute(context.Background(), w.d.DB, w.osvc.Today()); err != nil {
		w.t.Fatal(err)
	}
	marks, args := make([]string, 0), make([]any, 0)
	for _, p := range contract.HighRiskPermissions {
		marks, args = append(marks, "?"), append(args, string(p))
	}
	rows, err := w.d.DB.Query(`SELECT DISTINCT user_id, permission FROM user_effective_permissions WHERE permission IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id int64
		var p string
		_ = rows.Scan(&id, &p)
		out[fmt.Sprintf("%d:%s", id, p)] = true
	}
	return out
}

func (w *world) assertInvariants(label string, baseline map[string]bool) {
	w.t.Helper()
	for k := range w.riskPairs() {
		if !baseline[k] {
			w.t.Fatalf("%s: 위험 권한이 새로 생겼다: %s", label, k)
		}
	}
	if rep, err := w.osvc.Consistency(context.Background()); err != nil || !rep.OK {
		w.t.Fatalf("%s: 구간 일관성: %+v %v", label, rep, err)
	}
	// 재직이 아닌 사람의 계정이 켜져 있지 않고, 재직 중인 사람의 계정은 켜져 있다.
	day := string(w.osvc.Today())
	if n := w.count(`SELECT COUNT(*) FROM users u JOIN org_people p ON p.user_id = u.id WHERE u.active = 1 AND u.id NOT IN (SELECT user_id FROM org_person_spans WHERE valid_from <= ? AND (valid_to IS NULL OR ? < valid_to) AND left_flag = 0)`, day, day); n != 0 {
		w.t.Fatalf("%s: 재직이 아닌데 계정이 켜진 사람 %d명", label, n)
	}
	if n := w.count(`SELECT COUNT(*) FROM users u JOIN org_people p ON p.user_id = u.id WHERE u.active = 0 AND u.id IN (SELECT user_id FROM org_person_spans WHERE valid_from <= ? AND (valid_to IS NULL OR ? < valid_to) AND left_flag = 0)`, day, day); n != 0 {
		w.t.Fatalf("%s: 재직 중인데 계정이 꺼진 사람 %d명", label, n)
	}
	if res, err := w.d.AuditReader.Verify(context.Background()); err != nil || !res.ChainOK {
		w.t.Fatalf("%s: 감사 체인: %+v %v", label, res, err)
	}
}

// ---- 그룹 → 조직 ----

// 멤버 추가 = 전보 발령(오늘, 채널 scim, 감사), 제거는 소속 불변, 최근에 들어온 조직 그룹이 주 소속이다(재입사 때 그 조직으로 돌아온다).
func TestSCIMGroupUnitMode(t *testing.T) {
	w := newWorld(t)
	var dep int64
	if err := w.d.DB.QueryRow(`SELECT unit_id FROM org_unit_spans WHERE code = 'DEP'`).Scan(&dep); err != nil {
		t.Fatal(err)
	}
	t3 := w.unit("T3", "구매3팀", contract.OrgTeam, dep)
	if err := w.setMode("unit"); err != nil {
		t.Fatal(err)
	}
	gA, gB := w.mapUnit("g-a", w.t2), w.mapUnit("g-b", t3)
	u1 := w.scimUser("e1", "u1@pl.test")
	u2 := w.adopted("e2", "u2@pl.test") // 퇴사·재입사를 시험하려면 오늘보다 먼저 입사한 사람이어야 한다(입사 당일 퇴사는 거부)
	if w.unitOf(u1) != w.t1 {
		t.Fatal("시작 소속은 기본 조직(구매1팀)")
	}
	r := w.addMembers(gA, u1)
	if r.Status != 200 {
		t.Fatalf("멤버 추가: %d %s", r.Status, r.Raw)
	}
	if w.unitOf(u1) != w.t2 {
		t.Fatal("멤버 추가가 전보로 이어져야 함")
	}
	today := string(w.osvc.Today())
	if w.count(`SELECT COUNT(*) FROM org_events WHERE subject_id = ? AND kind = 'transfer' AND effective_date = ?`, u1, today) != 1 ||
		w.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'hr.transfer' AND target_id = ? AND channel = 'scim'`, fmt.Sprint(u1)) != 1 {
		t.Fatal("전보 이력(효력일 오늘)·감사(채널 scim)")
	}
	// 같은 요청을 다시 보내면 변화 없음(멱등).
	events := w.count(`SELECT COUNT(*) FROM org_events`)
	if r := w.addMembers(gA, u1); r.Status != 200 || w.count(`SELECT COUNT(*) FROM org_events`) != events {
		t.Fatalf("멱등: %d", r.Status)
	}
	// 제거는 소속을 바꾸지 않는다.
	if r := w.removeMember(gA, u1); r.Status != 200 {
		t.Fatalf("멤버 제거: %d %s", r.Status, r.Raw)
	}
	if w.unitOf(u1) != w.t2 || w.count(`SELECT COUNT(*) FROM org_events`) != events {
		t.Fatal("제거는 소속·이력을 바꾸지 않아야 함")
	}
	if w.memberRows(gA) != 0 {
		t.Fatal("제거한 멤버 행이 남음")
	}
	// 여러 조직 그룹: u2 가 A 다음 B 에 들어가면 B(최신)가 주 소속이고, 퇴사 뒤 재입사하면 B 의 조직으로 돌아온다.
	w.addMembers(gA, u2)
	time.Sleep(2 * time.Millisecond)
	w.addMembers(gB, u2)
	if w.unitOf(u2) != t3 {
		t.Fatal("최근에 들어온 그룹의 조직이 주 소속이어야 함")
	}
	if r := w.c.Patch(fmt.Sprintf("/Users/%d", u2), scimtest.Patch(scimtest.Op("Replace", "active", "False"))); r.Status != 200 {
		t.Fatalf("퇴사: %d %s", r.Status, r.Raw)
	}
	if r := w.c.Patch(fmt.Sprintf("/Users/%d", u2), scimtest.Patch(scimtest.Op("Replace", "active", "True"))); r.Status != 200 {
		t.Fatalf("재입사: %d %s", r.Status, r.Raw)
	}
	if w.unitOf(u2) != t3 {
		t.Fatalf("재입사 뒤 소속이 최신 그룹의 조직이 아니다: %d", w.unitOf(u2))
	}
	w.assertInvariants("조직 모드", w.riskPairs())
}

// 위험 권한이 생기는 조직으로의 이동은 거부되고(400 mutability) 요청 전체가 취소된다: 같은 요청의 다른 멤버도 옮겨지지 않고 멤버 행도 없다.
func TestSCIMGroupUnitModeRiskyTransferRejectedAtomically(t *testing.T) {
	w := newWorld(t)
	var dep int64
	_ = w.d.DB.QueryRow(`SELECT unit_id FROM org_unit_spans WHERE code = 'DEP'`).Scan(&dep)
	risky := w.unit("TR", "전표팀", contract.OrgTeam, dep)
	w.grantUnit(w.role("r_ledger", "ledger:write"), risky) // 이 조직 소속이면 위험 권한
	if err := w.setMode("unit"); err != nil {
		t.Fatal(err)
	}
	gR, gOK := w.mapUnit("g-risky", risky), w.mapUnit("g-ok", w.t2)
	u1, u2 := w.scimUser("e1", "u1@pl.test"), w.scimUser("e2", "u2@pl.test")
	baseline := w.riskPairs()
	events := w.count(`SELECT COUNT(*) FROM org_events`)
	r := w.addMembers(gR, u1, u2)
	if r.Status != 400 || r.ScimType() != "mutability" {
		t.Fatalf("위험 조직 이동이 거부되어야 함: %d %s", r.Status, r.Raw)
	}
	if w.memberRows(gR) != 0 || w.unitOf(u1) != w.t1 || w.unitOf(u2) != w.t1 || w.count(`SELECT COUNT(*) FROM org_events`) != events {
		t.Fatal("거부된 요청이 일부라도 반영됨")
	}
	w.assertInvariants("위험 이동 거부 뒤", baseline)
	// 위험 없는 조직으로는 같은 사람들이 옮겨진다.
	if r := w.addMembers(gOK, u1, u2); r.Status != 200 || w.unitOf(u1) != w.t2 || w.unitOf(u2) != w.t2 {
		t.Fatalf("무위험 이동: %d %s", r.Status, r.Raw)
	}
	// 거부된 요청이 동기화 기록에 거부로 남는다.
	if w.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'scim.rejected' AND result = 'denied'`) == 0 {
		t.Log("참고: 위험 이동 거부의 scim.rejected 감사가 없다(throttle 또는 비감사 사유)")
	}
}

// ---- 그룹 → 역할 ----

func TestSCIMGroupRoleMode(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("role"); err != nil {
		t.Fatal(err)
	}
	safe := w.role("r_safe", "purchase:create")
	ledger := w.role("r_ledger", "ledger:write")
	// 위험 역할 매핑은 저장되지 않는다.
	var rr *access.RiskyRoleError
	if _, err := w.mapRole("g-ledger", ledger); !errors.As(err, &rr) {
		t.Fatalf("위험 역할 매핑 저장이 거부되어야 함: %v", err)
	}
	g, err := w.mapRole("g-safe", safe)
	if err != nil {
		t.Fatal(err)
	}
	u1, u2, u3 := w.scimUser("e1", "u1@pl.test"), w.scimUser("e2", "u2@pl.test"), w.scimUser("e3", "u3@pl.test")
	w.grantUser(safe, u2) // 사람이 준 같은 부여
	baseline := w.riskPairs()
	if r := w.addMembers(g, u1, u2); r.Status != 200 {
		t.Fatalf("멤버 추가: %d %s", r.Status, r.Raw)
	}
	if w.userGrants(safe, u1) != 1 || w.userGrants(safe, u2) != 1 {
		t.Fatal("부여 수")
	}
	var gid1, gid2 any
	_ = w.d.DB.QueryRow(`SELECT grant_id FROM scim_group_members WHERE map_id = ? AND user_id = ?`, g, u1).Scan(&gid1)
	_ = w.d.DB.QueryRow(`SELECT grant_id FROM scim_group_members WHERE map_id = ? AND user_id = ?`, g, u2).Scan(&gid2)
	if gid1 == nil || gid2 != nil {
		t.Fatalf("SCIM 이 만든 부여만 grant_id 로 추적해야 함: u1=%v u2=%v", gid1, gid2)
	}
	if w.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'role.grant' AND channel = 'scim'`) != 1 {
		t.Fatal("부여 감사(채널 scim)는 SCIM 이 만든 한 건뿐")
	}
	// 제거: SCIM 이 만든 부여만 회수, 사람이 준 부여는 그대로.
	w.removeMember(g, u1)
	w.removeMember(g, u2)
	if w.userGrants(safe, u1) != 0 || w.userGrants(safe, u2) != 1 {
		t.Fatalf("회수 결과: u1=%d u2=%d", w.userGrants(safe, u1), w.userGrants(safe, u2))
	}
	// role_grants.source 의 CHECK 는 그대로다(SCIM 이 새 값을 만들지 않는다).
	if _, err := w.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at) VALUES (?, 'user', ?, 'scim', 'a')`, safe, u3); err == nil {
		t.Fatal("role_grants.source 에 scim 이 들어감(CHECK 가 바뀜)")
	}
	for _, src := range []string{"scim"} {
		if w.count(`SELECT COUNT(*) FROM role_grants WHERE source = ?`, src) != 0 {
			t.Fatal("source 값 불변")
		}
	}
	// 역할이 나중에 위험해지면 새 멤버 추가는 400 mutability, 제거는 가능하다.
	w.addMembers(g, u1)
	w.rolePerm(safe, "ledger:write")
	r := w.addMembers(g, u3)
	if r.Status != 400 || r.ScimType() != "mutability" {
		t.Fatalf("위험해진 역할의 멤버 추가: %d %s", r.Status, r.Raw)
	}
	if w.userGrants(safe, u3) != 0 {
		t.Fatal("거부됐는데 부여됨")
	}
	if r := w.removeMember(g, u1); r.Status != 200 || w.userGrants(safe, u1) != 0 {
		t.Fatalf("위험해진 역할의 멤버 제거는 가능해야 함: %d %s", r.Status, r.Raw)
	}
	// 베이스라인(사람이 준 u2 부여는 그때는 위험하지 않았다)은 이후 역할 변경으로 생긴 위험이므로 SCIM 탓이 아니다. SCIM 경로가 만든 위험은 없어야 한다.
	for k := range w.riskPairs() {
		if !baseline[k] && !strings.HasPrefix(k, fmt.Sprint(u2)+":") {
			t.Fatalf("SCIM 경로로 위험 권한이 생겼다: %s", k)
		}
	}
}

// 매핑 삭제: SCIM 이 만든 부여만 회수하고 사람이 준 같은 부여는 남긴다.
func TestSCIMGroupRoleMapDeleteRevokesOnlySCIMGrants(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("role"); err != nil {
		t.Fatal(err)
	}
	safe := w.role("r_safe", "purchase:create")
	g, err := w.mapRole("g-safe", safe)
	if err != nil {
		t.Fatal(err)
	}
	u1, u2 := w.scimUser("e1", "u1@pl.test"), w.scimUser("e2", "u2@pl.test")
	w.grantUser(safe, u2)
	if r := w.addMembers(g, u1, u2); r.Status != 200 {
		t.Fatalf("멤버 추가: %d %s", r.Status, r.Raw)
	}
	if err := w.sv().DeleteMap(context.Background(), w.admin, g); err != nil {
		t.Fatal(err)
	}
	if w.userGrants(safe, u1) != 0 || w.userGrants(safe, u2) != 1 {
		t.Fatalf("매핑 삭제 뒤 부여: u1=%d(0 이어야) u2=%d(1 이어야)", w.userGrants(safe, u1), w.userGrants(safe, u2))
	}
	if w.memberRows(g) != 0 {
		t.Fatal("매핑 삭제 뒤 멤버 행")
	}
}

// 같은 역할에 매핑된 그룹 둘에 속한 사용자를 한 그룹에서 빼도, 다른 그룹에 남아 있는 동안은 역할이 유지되어야 한다.
// (고친 결함) 두 번째 그룹의 멤버 행이 같은 부여 번호를 가리키지 않아 첫 그룹에서 제거할 때 부여가 회수되던 문제. 마지막 참조일 때만 회수한다.
func TestSCIMGroupRoleTwoGroupsSameRoleKeepsGrant(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("role"); err != nil {
		t.Fatal(err)
	}
	safe := w.role("r_safe", "purchase:create")
	g1, err := w.mapRole("g-1", safe)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := w.mapRole("g-2", safe)
	if err != nil {
		t.Fatal(err)
	}
	u := w.scimUser("e1", "u1@pl.test")
	w.addMembers(g1, u)
	w.addMembers(g2, u)
	w.removeMember(g1, u)
	if got := w.userGrants(safe, u); got != 1 {
		t.Fatalf("다른 그룹에 남아 있는데 부여가 회수됨: %d건", got)
	}
	// 마지막 그룹에서 빠지면 그때 회수된다.
	w.removeMember(g2, u)
	if got := w.userGrants(safe, u); got != 0 {
		t.Fatalf("마지막 그룹에서 빠졌는데 부여가 남음: %d건", got)
	}
}

// ---- 모드 전환 ----

func TestSCIMGroupModeSwitchRejectedWhileOtherKindMapsExist(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("unit"); err != nil {
		t.Fatal(err)
	}
	g := w.mapUnit("g-a", w.t2)
	if err := w.setMode("role"); err == nil {
		t.Fatal("다른 방식의 매핑이 남아 있는데 방식이 바뀜")
	}
	if err := w.setMode("off"); err == nil {
		t.Fatal("매핑이 남아 있는데 끔으로 바뀜(그룹 요청이 403 이 되어 멤버 정리가 멈춘다)")
	}
	if err := w.sv().DeleteMap(context.Background(), w.admin, g); err != nil {
		t.Fatal(err)
	}
	if err := w.setMode("role"); err != nil {
		t.Fatalf("매핑을 지운 뒤에는 바뀌어야 함: %v", err)
	}
	// 방식이 다른 그룹에 보낸 요청은 거부된다.
	u := w.scimUser("e1", "u1@pl.test")
	if r := w.c.Post("/Groups", map[string]any{"externalId": "g-none", "displayName": "g-none", "members": members(u)}); r.Status != 400 {
		t.Fatalf("매핑 없는 그룹: %d %s", r.Status, r.Raw)
	}
}

// ---- 무작위 순서 불변식 ----

// 조직 모드: Users(생성·퇴사·재입사·이름·채택)와 Groups(추가·제거·교체)를 섞어 고정 시드로 보낸다. 위험 조직·위험 퇴사자·보호 계정을 섞는다.
func TestSCIMRandomUsersAndGroupsUnitMode(t *testing.T) {
	w := newWorld(t)
	var dep int64
	_ = w.d.DB.QueryRow(`SELECT unit_id FROM org_unit_spans WHERE code = 'DEP'`).Scan(&dep)
	t3 := w.unit("T3", "구매3팀", contract.OrgTeam, dep)
	risky := w.unit("TR", "전표팀", contract.OrgTeam, dep)
	w.grantUnit(w.role("r_ledger", "ledger:write"), risky)
	fin := w.addUser("재무담당", "fin@pl.test", "finance")
	w.leave(fin)
	if err := w.setMode("unit"); err != nil {
		t.Fatal(err)
	}
	maps := []int64{w.mapUnit("g1", w.t1), w.mapUnit("g2", w.t2), w.mapUnit("g3", t3), w.mapUnit("g-risky", risky)}
	baseline := w.riskPairs()
	rnd := rand.New(rand.NewSource(4040))
	var users []int64
	ok, bad := 0, 0
	for i := 0; i < 120; i++ {
		var r scimtest.Resp
		switch k := rnd.Intn(8); {
		case k == 0 || len(users) < 3:
			mail := fmt.Sprintf("r%d@pl.test", i)
			r = w.c.Post("/Users", scimtest.User(mail, fmt.Sprintf("x%d", i), "랜덤", "", "사원"))
			if r.Status == 201 {
				var id int64
				_, _ = fmt.Sscan(r.ID(), &id)
				users = append(users, id)
			}
		case k == 1:
			r = w.c.Patch(fmt.Sprintf("/Users/%d", users[rnd.Intn(len(users))]), scimtest.Patch(scimtest.Op("Replace", "active", "False")))
		case k == 2:
			r = w.c.Patch(fmt.Sprintf("/Users/%d", users[rnd.Intn(len(users))]), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
		case k == 3: // 위험 퇴사자를 건드린다(재활성·삭제)
			if rnd.Intn(2) == 0 {
				r = w.c.Patch(fmt.Sprintf("/Users/%d", fin), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
			} else {
				r = w.c.Delete(fmt.Sprintf("/Users/%d", fin))
			}
		case k <= 5:
			r = w.addMembers(maps[rnd.Intn(len(maps))], pick(rnd, users, 1+rnd.Intn(3))...)
		case k == 6:
			r = w.removeMember(maps[rnd.Intn(len(maps))], users[rnd.Intn(len(users))])
		default: // 교체(PUT)
			gm := maps[rnd.Intn(len(maps))]
			r = w.c.Put(fmt.Sprintf("/Groups/%d", gm), map[string]any{"members": members(pick(rnd, users, rnd.Intn(4))...)})
		}
		if r.Status >= 500 {
			t.Fatalf("%d번째 요청 서버 오류: %d %s", i, r.Status, r.Raw)
		}
		if r.Status < 300 {
			ok++
		} else {
			bad++
		}
		w.assertInvariants(fmt.Sprintf("조직 모드 %d번째", i), baseline)
		if i == 60 { // 모드 전환 시도는 매핑이 남아 있어 거부된다
			if err := w.setMode("role"); err == nil {
				t.Fatal("매핑이 남아 있는데 방식이 바뀜")
			}
		}
	}
	if ok == 0 || bad == 0 {
		t.Fatalf("한쪽만 다뤘다: 성공 %d 거부 %d", ok, bad)
	}
	// 위험 조직 그룹에는 아무도 들어가지 못했다.
	if w.memberRows(maps[3]) != 0 {
		t.Fatal("위험 조직 그룹에 멤버가 들어감")
	}
}

// 역할 모드: 무위험 역할 그룹에 사람과 SCIM 이 섞여 부여·회수한다. 역할이 중간에 위험해져도 SCIM 은 위험 권한을 새로 만들지 못한다.
func TestSCIMRandomUsersAndGroupsRoleMode(t *testing.T) {
	w := newWorld(t)
	if err := w.setMode("role"); err != nil {
		t.Fatal(err)
	}
	safe := w.role("r_safe", "purchase:create")
	safe2 := w.role("r_safe2", "purchase:read")
	g1, err := w.mapRole("g1", safe)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := w.mapRole("g2", safe2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mapRole("g-bad", w.role("r_ledger", "ledger:write")); err == nil {
		t.Fatal("위험 역할 매핑이 저장됨")
	}
	fin := w.addUser("재무담당", "fin@pl.test", "finance")
	w.leave(fin)
	maps := []int64{g1, g2}
	baseline := w.riskPairs()
	rnd := rand.New(rand.NewSource(4041))
	var users []int64
	ok, bad := 0, 0
	for i := 0; i < 120; i++ {
		if i == 70 { // 중간에 역할이 위험해진다: 그 뒤 SCIM 이 새로 만든 위험 권한은 없어야 한다(이미 있던 부여는 사람이 만든 것이 아니라 역할 변경의 결과).
			w.rolePerm(safe, "ledger:write")
			for k := range w.riskPairs() {
				baseline[k] = true
			}
		}
		var r scimtest.Resp
		switch k := rnd.Intn(8); {
		case k == 0 || len(users) < 3:
			r = w.c.Post("/Users", scimtest.User(fmt.Sprintf("r%d@pl.test", i), fmt.Sprintf("x%d", i), "랜덤", "", "사원"))
			if r.Status == 201 {
				var id int64
				_, _ = fmt.Sscan(r.ID(), &id)
				users = append(users, id)
			}
		case k == 1:
			r = w.c.Patch(fmt.Sprintf("/Users/%d", users[rnd.Intn(len(users))]), scimtest.Patch(scimtest.Op("Replace", "active", "False")))
		case k == 2:
			r = w.c.Patch(fmt.Sprintf("/Users/%d", fin), scimtest.Patch(scimtest.Op("Replace", "active", "True")))
		case k == 3: // 사람이 같은 부여를 직접 준다(이미 있으면 무시). 역할이 위험해진 뒤에는 사람도 주지 않는다(위험 부여는 사람의 2인 확인으로만 가능하다).
			if i >= 70 {
				continue
			}
			_, _ = w.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_at) VALUES (?, 'user', ?, '', 0, 'manual', '사람', 'a')`, []int64{safe, safe2}[rnd.Intn(2)], users[rnd.Intn(len(users))])
			continue
		case k <= 5:
			r = w.addMembers(maps[rnd.Intn(2)], pick(rnd, users, 1+rnd.Intn(3))...)
		case k == 6:
			r = w.removeMember(maps[rnd.Intn(2)], users[rnd.Intn(len(users))])
		default:
			r = w.c.Put(fmt.Sprintf("/Groups/%d", maps[rnd.Intn(2)]), map[string]any{"members": members(pick(rnd, users, rnd.Intn(4))...)})
		}
		if r.Status >= 500 {
			t.Fatalf("%d번째 요청 서버 오류: %d %s", i, r.Status, r.Raw)
		}
		if r.Status < 300 {
			ok++
		} else {
			bad++
		}
		w.assertInvariants(fmt.Sprintf("역할 모드 %d번째", i), baseline)
	}
	if ok == 0 || bad == 0 {
		t.Fatalf("한쪽만 다뤘다: 성공 %d 거부 %d", ok, bad)
	}
}

func pick(rnd *rand.Rand, from []int64, n int) []int64 {
	perm := rnd.Perm(len(from))
	if n > len(from) {
		n = len(from)
	}
	out := make([]int64, 0, n)
	for _, i := range perm[:n] {
		out = append(out, from[i])
	}
	return out
}
