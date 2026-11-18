package main

// B40 3단계: SCIM Groups(조직·역할 모드), 그룹 매핑 관리, 위험 역할 불가(AC-4018).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

func (s *scimEnv) enableMode(mode string) {
	s.t.Helper()
	code, body := s.call(s.admin, "PUT", "/api/admin/scim/settings", fmt.Sprintf(`{"enabled":true,"groupMode":%q,"defaultUnit":"T-PU1","defaultRank":"사원","defaultRole":"staff","adoptExisting":true}`, mode), s.hdr)
	if code != 200 {
		s.t.Fatalf("설정 저장(%s): %d %s", mode, code, body)
	}
}

func (s *scimEnv) mapGroup(source, ext, display string, unitCode string, roleKey string) (int, string) {
	s.t.Helper()
	var unitID, roleID int64
	if unitCode != "" {
		_ = s.d.DB.QueryRow(`SELECT id FROM org_units WHERE code = ?`, unitCode).Scan(&unitID)
	}
	if roleKey != "" {
		_ = s.d.DB.QueryRow(`SELECT id FROM roles WHERE key = ?`, roleKey).Scan(&roleID)
	}
	return s.call(s.admin, "POST", "/api/admin/scim/group-maps", fmt.Sprintf(`{"source":%q,"groupExternalId":%q,"displayName":%q,"unitId":%d,"roleId":%d}`, source, ext, display, unitID, roleID), s.hdr)
}

func member(ids ...string) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"value": id})
	}
	return out
}

func group(ext, display string, ids ...string) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "externalId": ext, "displayName": display, "members": member(ids...)}
}

func TestSCIMGroupsOff(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enable()
	g := s.client(s.token("entra", "users_groups"))
	if r := g.Get("/Groups"); r.Status != 200 || r.Total() != 0 {
		t.Errorf("꺼진 상태 목록: %d %s", r.Status, r.Raw)
	}
	if r := g.Post("/Groups", group("g1", "구매팀")); r.Status != 403 {
		t.Errorf("꺼진 상태 쓰기: %d", r.Status)
	}
	// 매핑은 연동 방식이 정해진 뒤에만 만들 수 있다.
	if code, _ := s.mapGroup("entra", "g1", "구매팀", "T-PU2", ""); code != 400 {
		t.Errorf("방식 없이 매핑: %d", code)
	}
}

func TestSCIMGroupUnitMode(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("unit")
	c := s.client(s.token("entra", "users_groups"))
	u1 := c.Post("/Users", scimtest.User("grp.a@corp.test", "ext-ga", "그룹 가", "T-PU1", "사원"))
	u2 := c.Post("/Users", scimtest.User("grp.b@corp.test", "ext-gb", "그룹 나", "T-PU1", "사원"))
	if u1.Status != 201 || u2.Status != 201 {
		t.Fatalf("사용자: %d %d", u1.Status, u2.Status)
	}
	// 매핑이 없는 그룹은 거부되고 화면의 "매핑이 없는 그룹"에 나타난다.
	if r := c.Post("/Groups", group("g-pu2", "구매2팀 그룹", u1.ID())); r.Status != 400 || r.ScimType() != "invalidValue" {
		t.Fatalf("매핑 없는 그룹: %d %s", r.Status, r.Raw)
	}
	if code, body := s.call(s.admin, "GET", "/api/admin/scim/group-maps", "", nil); code != 200 || !strings.Contains(body, `"groupExternalId":"g-pu2"`) {
		t.Errorf("매핑 없는 그룹 목록: %d %s", code, body)
	}
	// 조직 모드에서는 조직만 고를 수 있다(역할 지정은 무시되고 조직이 없으면 400).
	if code, _ := s.mapGroup("entra", "g-pu2", "구매2팀 그룹", "", "manager"); code != 400 {
		t.Errorf("조직 없이 매핑: %d", code)
	}
	if code, body := s.mapGroup("entra", "g-pu2", "구매2팀 그룹", "T-PU2", ""); code != 201 {
		t.Fatalf("매핑: %d %s", code, body)
	}
	if code, _ := s.mapGroup("entra", "g-pu2", "구매2팀 그룹", "T-PU2", ""); code != 409 {
		t.Errorf("중복 매핑: %d", code)
	}
	// 매핑이 생기면 POST 가 성공하고 멤버가 그 조직으로 전보된다(효력일 오늘).
	r := c.Post("/Groups", group("g-pu2", "구매2팀 그룹", u1.ID()))
	if r.Status != 201 {
		t.Fatalf("그룹 생성: %d %s", r.Status, r.Raw)
	}
	gid := r.ID()
	if s.events("transfer", u1.ID()) != 1 {
		t.Error("그룹 가입이 전보를 만들지 않음")
	}
	if v := c.Get("/Users/" + u1.ID()); v.Status != 200 {
		t.Fatalf("조회: %d", v.Status)
	} else if ent, _ := v.Body[scimtest.EnterpriseSchema].(map[string]any); ent["department"] != "구매2팀" {
		t.Errorf("전보 뒤 부서: %v", ent)
	} else if gs, _ := v.Body["groups"].([]any); len(gs) != 1 {
		t.Errorf("사용자의 groups: %v", v.Body["groups"])
	}
	// PATCH: 멤버 추가·제거. 두 번째 사람 추가, 첫 사람 제거(제거는 조직을 바꾸지 않는다).
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(u2.ID())))); p.Status != 200 {
		t.Fatalf("멤버 추가: %d %s", p.Status, p.Raw)
	}
	if s.events("transfer", u2.ID()) != 1 {
		t.Error("추가한 멤버가 전보되지 않음")
	}
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("remove", `members[value eq "`+u1.ID()+`"]`, ""))); p.Status != 200 {
		t.Fatalf("멤버 제거: %d %s", p.Status, p.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM scim_group_members WHERE user_id = ?`, u1.ID()) != 0 || s.events("transfer", u1.ID()) != 1 {
		t.Error("제거가 멤버 행을 남기거나 조직을 바꿈")
	}
	// 같은 멤버를 다시 추가하면 변경 없음(멱등), 조직이 이미 같으면 전보도 없다.
	before := s.count(`SELECT COUNT(*) FROM org_events`)
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(u2.ID())))); p.Status != 200 || s.count(`SELECT COUNT(*) FROM org_events`) != before {
		t.Errorf("멱등 추가: %d", p.Status)
	}
	// 조직 모드에서 사용자의 부서 속성은 무시된다(그룹 소속이 정한다).
	if p := c.Put("/Users/"+u2.ID(), scimtest.User("grp.b@corp.test", "ext-gb", "그룹 나", "T-PU1", "사원")); p.Status != 200 || s.events("transfer", u2.ID()) != 1 {
		t.Errorf("부서 속성 무시: %d", p.Status)
	}
	// 연결되지 않은 사용자·존재하지 않는 사용자·보호 계정은 멤버가 될 수 없고 요청 전체가 취소된다.
	var adminID string
	_ = s.d.DB.QueryRow(`SELECT CAST(id AS TEXT) FROM users WHERE email = 'admin@pl.test'`).Scan(&adminID)
	for name, id := range map[string]string{"미연결 보호 계정": adminID, "없는 사용자": "99999"} {
		if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(id)))); p.Status != 400 {
			t.Errorf("%s: %d %s", name, p.Status, p.Raw)
		}
	}
	// 그룹 조회(필터)와 GET.
	if l := c.Get("/Groups" + scimtest.Filter(`displayName eq "구매2팀 그룹"`)); l.Status != 200 || l.Total() != 1 {
		t.Errorf("그룹 필터: %d %s", l.Status, l.Raw)
	}
	if l := c.Get("/Groups" + scimtest.Filter(`externalId eq "g-pu2"`)); l.Total() != 1 {
		t.Errorf("그룹 externalId 필터: %s", l.Raw)
	}
	if l := c.Get("/Groups" + scimtest.Filter(`members co "x"`)); l.Status != 400 || l.ScimType() != "invalidFilter" {
		t.Errorf("미지원 그룹 필터: %d", l.Status)
	}
	if g := c.Get("/Groups/" + gid); g.Status != 200 || g.Body["displayName"] != "구매2팀 그룹" {
		t.Errorf("그룹 단건: %d %s", g.Status, g.Raw)
	}
	// 방식은 다른 방식의 매핑이 남아 있으면 바꿀 수 없다.
	if code, _ := s.call(s.admin, "PUT", "/api/admin/scim/settings", `{"enabled":true,"groupMode":"role","defaultUnit":"T-PU1","defaultRank":"사원","defaultRole":"staff","adoptExisting":true}`, s.hdr); code != 400 {
		t.Errorf("매핑이 남은 채 방식 변경: %d", code)
	}
	// 그룹 삭제는 멤버 전원 제거(매핑은 남는다).
	if d := c.Delete("/Groups/" + gid); d.Status != 204 || s.count(`SELECT COUNT(*) FROM scim_group_members`) != 0 || s.count(`SELECT COUNT(*) FROM scim_group_maps`) != 1 {
		t.Errorf("그룹 삭제: %d", d.Status)
	}
	// 매핑 삭제: 감사에 남는다.
	var mapID int64
	_ = s.d.DB.QueryRow(`SELECT id FROM scim_group_maps`).Scan(&mapID)
	if code, b := s.call(s.admin, "DELETE", fmt.Sprintf("/api/admin/scim/group-maps/%d", mapID), "", s.hdr); code != 204 {
		t.Fatalf("매핑 삭제: %d %s", code, b)
	}
	if s.auditCount("scim.group_map_set", "success") != 1 || s.auditCount("scim.group_map_delete", "success") != 1 {
		t.Error("매핑 감사가 없음")
	}
	assertChainOK(t, s)
}

func TestSCIMGroupRoleModeNeverRisky(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("role")
	c := s.client(s.token("okta", "users_groups"))
	u1 := c.Post("/Users", scimtest.User("role.a@corp.test", "", "역할 가", "T-PU1", "사원"))
	u2 := c.Post("/Users", scimtest.User("role.b@corp.test", "", "역할 나", "T-PU1", "사원"))
	if u1.Status != 201 || u2.Status != 201 {
		t.Fatalf("사용자: %d %d", u1.Status, u2.Status)
	}
	// 위험 권한이 든 역할은 매핑 자체가 거부된다(admin·finance 템플릿).
	for _, key := range []string{"admin", "finance"} {
		if code, body := s.mapGroup("okta", "g-"+key, key, "", key); code != 400 {
			t.Errorf("위험 역할 %s 매핑: %d %s", key, code, body)
		}
	}
	if s.count(`SELECT COUNT(*) FROM scim_group_maps`) != 0 {
		t.Fatal("위험 역할 매핑이 저장됨")
	}
	if code, body := s.mapGroup("okta", "g-mgr", "결재자 그룹", "", "manager"); code != 201 {
		t.Fatalf("일반 역할 매핑: %d %s", code, body)
	}
	var roleID int64
	_ = s.d.DB.QueryRow(`SELECT id FROM roles WHERE key = 'manager'`).Scan(&roleID)
	// 멤버 추가 = 사용자 대상 직접 부여(role_grants), 감사 role.grant(채널 scim), 부여 번호 추적.
	r := c.Post("/Groups", group("g-mgr", "결재자 그룹", u1.ID()))
	if r.Status != 201 {
		t.Fatalf("그룹: %d %s", r.Status, r.Raw)
	}
	gid := r.ID()
	var grantID int64
	if err := s.d.DB.QueryRow(`SELECT grant_id FROM scim_group_members WHERE user_id = ?`, u1.ID()).Scan(&grantID); err != nil || grantID == 0 {
		t.Fatalf("부여 번호 추적: %d %v", grantID, err)
	}
	if s.count(`SELECT COUNT(*) FROM role_grants WHERE id = ? AND target_type='user' AND source='manual' AND role_id = ?`, grantID, roleID) != 1 {
		t.Error("사용자 대상 직접 부여가 없음")
	}
	if s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='role.grant' AND channel='scim'`) != 1 {
		t.Error("role.grant 감사(채널 scim)가 1건이 아님")
	}
	if s.count(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = ? AND role_id = ?`, u1.ID(), roleID) == 0 {
		t.Error("실효 권한에 반영되지 않음")
	}
	// 사람이 이미 준 같은 부여는 SCIM 이 만들지도 회수하지도 않는다.
	if _, err := s.d.DB.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, include_descendants, source, reason, created_at) VALUES (?, 'user', ?, 0, 'manual', '사람이 줌', '2026-11-01T00:00:00Z')`, roleID, u2.ID()); err != nil {
		t.Fatal(err)
	}
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(u2.ID())))); p.Status != 200 {
		t.Fatalf("이미 있는 부여: %d %s", p.Status, p.Raw)
	}
	var g2 any
	_ = s.d.DB.QueryRow(`SELECT grant_id FROM scim_group_members WHERE user_id = ?`, u2.ID()).Scan(&g2)
	if g2 != nil {
		t.Errorf("사람이 준 부여를 추적함: %v", g2)
	}
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("remove", `members[value eq "`+u2.ID()+`"]`, ""))); p.Status != 200 {
		t.Fatalf("제거: %d", p.Status)
	}
	if s.count(`SELECT COUNT(*) FROM role_grants WHERE target_id = ? AND role_id = ?`, u2.ID(), roleID) != 1 {
		t.Error("SCIM 이 사람이 준 부여를 회수함")
	}
	// 역할이 나중에 위험 권한을 얻으면 새 멤버 추가는 거부된다(제거는 계속 된다).
	if _, err := s.d.DB.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, 'ledger:write')`, roleID); err != nil {
		t.Fatal(err)
	}
	u3 := c.Post("/Users", scimtest.User("role.c@corp.test", "", "역할 다", "T-PU1", "사원"))
	before := s.count(`SELECT COUNT(*) FROM role_grants`)
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(u3.ID())))); p.Status != 400 || p.ScimType() != "mutability" {
		t.Errorf("위험해진 역할의 멤버 추가: %d %s", p.Status, p.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM role_grants`) != before {
		t.Error("거부됐는데 부여가 만들어짐")
	}
	if s.auditCount("scim.rejected", "denied") < 1 {
		t.Error("거부 감사가 없음")
	}
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("remove", `members[value eq "`+u1.ID()+`"]`, ""))); p.Status != 200 {
		t.Errorf("위험해진 역할의 멤버 제거: %d %s", p.Status, p.Raw)
	}
	if s.count(`SELECT COUNT(*) FROM role_grants WHERE id = ?`, grantID) != 0 {
		t.Error("제거가 부여를 회수하지 않음")
	}
	// 매핑을 지우면 SCIM 이 만든 부여만 회수한다.
	if _, err := s.d.DB.Exec(`DELETE FROM role_permissions WHERE role_id = ? AND permission = 'ledger:write'`, roleID); err != nil {
		t.Fatal(err)
	}
	if p := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("add", "members", member(u1.ID(), u3.ID())))); p.Status != 200 {
		t.Fatalf("다시 추가: %d %s", p.Status, p.Raw)
	}
	var mapID int64
	_ = s.d.DB.QueryRow(`SELECT id FROM scim_group_maps`).Scan(&mapID)
	if code, b := s.call(s.admin, "DELETE", fmt.Sprintf("/api/admin/scim/group-maps/%d", mapID), "", s.hdr); code != 204 {
		t.Fatalf("매핑 삭제: %d %s", code, b)
	}
	if s.count(`SELECT COUNT(*) FROM scim_group_members`) != 0 || s.count(`SELECT COUNT(*) FROM role_grants WHERE source='manual' AND reason LIKE 'SCIM 그룹%'`) != 0 {
		t.Error("매핑 삭제가 SCIM 부여를 남김")
	}
	if s.count(`SELECT COUNT(*) FROM role_grants WHERE target_id = ? AND role_id = ?`, u2.ID(), roleID) != 1 {
		t.Error("매핑 삭제가 사람이 준 부여를 지움")
	}
	assertChainOK(t, s)
}

func TestSCIMGroupAdminPermissions(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("unit")
	aud := s.browser()
	s.login(aud, "auditor@pl.test")
	h := map[string]string{"X-CSRF-Token": s.csrf(aud), "Content-Type": "application/json"}
	if code, body := s.call(aud, "GET", "/api/admin/scim/group-maps", "", nil); code != 200 || !strings.Contains(body, `"units"`) {
		t.Errorf("감사자 조회: %d", code)
	}
	if code, _ := s.call(aud, "POST", "/api/admin/scim/group-maps", `{"source":"entra","groupExternalId":"x","unitId":1}`, h); code != 403 {
		t.Errorf("감사자 매핑 생성: %d", code)
	}
	// Bearer(SCIM 토큰·개인 토큰)로는 매핑을 만들 수 없다.
	tok := s.token("entra", "users_groups")
	if code, _ := s.call(s.browser(), "POST", "/api/admin/scim/group-maps", `{"source":"entra","groupExternalId":"x","unitId":1}`, map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}); code == 200 || code == 201 {
		t.Errorf("Bearer 매핑 생성: %d", code)
	}
	var list struct {
		Roles []struct {
			Key   string
			Risky bool
		}
	}
	_, body := s.call(s.admin, "GET", "/api/admin/scim/group-maps", "", nil)
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	risky := map[string]bool{}
	for _, r := range list.Roles {
		risky[r.Key] = r.Risky
	}
	if !risky["admin"] || !risky["finance"] || risky["manager"] || risky["staff"] {
		t.Errorf("역할 선택지의 위험 표시: %v", risky)
	}
}
