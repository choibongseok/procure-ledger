package main

// B40 모의 클라이언트 세트(명세 15절): Entra 형·Okta 형·규격형 세 종류가 같은 시나리오 표로 서버를 돈다.
// SQLite 가 기본이고 PL_TEST_DB=pg(make verify-pg)면 같은 세트가 PostgreSQL 에서 돈다.

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/scimtest"
)

func TestSCIMConformanceEntra(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("unit")
	c := s.client(s.token("entra", "users_groups"))
	// 주기 1: 신규 → 생성(조회 후 POST).
	r := c.EntraSync("oid-1", "e.one@corp.test", "엔트라 하나", "T-PU1", "사원", true)
	if r.Status != 201 {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	// 주기 2: 같은 상태(조회 → PATCH replace active "True")는 변경 없음.
	logs := s.count(`SELECT COUNT(*) FROM scim_sync_log`)
	if r := c.EntraSync("oid-1", "e.one@corp.test", "엔트라 하나", "T-PU1", "사원", true); r.Status != 200 || s.count(`SELECT COUNT(*) FROM scim_sync_log`) != logs {
		t.Fatalf("변경 없음: %d", r.Status)
	}
	// 이름 변경, 연락처 변경.
	if r := c.Patch("/Users/"+id, scimtest.Patch(scimtest.Op("Replace", "displayName", "엔트라 새이름"), scimtest.Op("Replace", `phoneNumbers[type eq "work"].value`, "010-1111-2222"))); r.Status != 200 || r.Body["displayName"] != "엔트라 새이름" {
		t.Fatalf("이름·연락처: %d %s", r.Status, r.Raw)
	}
	// 그룹: 매핑 → 그룹 생성(POST) → 멤버 추가·제거(PATCH).
	if code, b := s.mapGroup("entra", "grp-oid", "구매2팀", "T-PU2", ""); code != 201 {
		t.Fatalf("매핑: %d %s", code, b)
	}
	if g := c.Get("/Groups" + scimtest.Filter(`displayName eq "구매2팀"`)); g.Total() != 1 {
		t.Fatalf("그룹 조회: %s", g.Raw)
	}
	gid := c.Get("/Groups" + scimtest.Filter(`externalId eq "grp-oid"`)).Resources()[0]["id"].(string)
	if g := c.Patch("/Groups/"+gid, scimtest.Patch(scimtest.Op("Add", "members", member(id)))); g.Status != 200 || s.events("transfer", id) != 1 {
		t.Fatalf("그룹 멤버 추가: %d %s", g.Status, g.Raw)
	}
	// 비활성화(문자열 "False") → 퇴사. 입사 당일이라 409 이고, 그대로 두면 다음 주기에도 같다.
	if r := c.EntraSync("oid-1", "e.one@corp.test", "엔트라 새이름", "T-PU1", "사원", false); r.Status != 409 {
		t.Errorf("입사 당일 비활성화: %d %s", r.Status, r.Raw)
	}
	assertChainOK(t, s)
}

func TestSCIMConformanceOkta(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("role")
	c := s.client(s.token("okta", "users_groups"))
	// Okta: userName 조회 → 없으면 POST(externalId 없음) → 다음 주기는 PUT.
	r := c.OktaSync("o.one@corp.test", "오크타 하나", "T-PU1", "사원", true)
	if r.Status != 201 {
		t.Fatalf("생성: %d %s", r.Status, r.Raw)
	}
	id := r.ID()
	if r := c.OktaSync("o.one@corp.test", "오크타 하나", "T-PU1", "사원", true); r.Status != 200 {
		t.Fatalf("PUT 주기: %d %s", r.Status, r.Raw)
	}
	// 경로 없는 replace 로 부서·직위 변경(Okta 의 프로필 변경 모양).
	body := scimtest.Patch(scimtest.Op("replace", "", map[string]any{"title": "대리", scimtest.EnterpriseSchema: map[string]any{"department": "T-PU2"}}))
	if r := c.Patch("/Users/"+id, body); r.Status != 200 || r.Body["title"] != "대리" || s.events("transfer", id) != 1 || s.events("rank_change", id) != 1 {
		t.Fatalf("부서·직위 변경: %d %s", r.Status, r.Raw)
	}
	// Push Groups: 역할 매핑 그룹에 멤버를 보낸다. 위험 역할은 매핑 자체가 안 된다.
	if code, _ := s.mapGroup("okta", "00g-mgr", "결재자", "", "admin"); code != 400 {
		t.Errorf("위험 역할 매핑: %d", code)
	}
	if code, b := s.mapGroup("okta", "00g-mgr", "결재자", "", "manager"); code != 201 {
		t.Fatalf("매핑: %d %s", code, b)
	}
	g := c.Post("/Groups", group("00g-mgr", "결재자", id))
	if g.Status != 201 || s.count(`SELECT COUNT(*) FROM audit_logs WHERE action='role.grant' AND channel='scim'`) != 1 {
		t.Fatalf("그룹 푸시: %d %s", g.Status, g.Raw)
	}
	// 비활성화는 active=false(경로 없는 replace)이고, 입사 당일이 아닌 사용자(채택)는 퇴사가 된다.
	adopted := c.OktaSync(s.seedStaff(5), "", "", "", true)
	if adopted.Status != 200 && adopted.Status != 201 {
		t.Fatalf("채택: %d %s", adopted.Status, adopted.Raw)
	}
	if r := c.Patch("/Users/"+adopted.ID(), scimtest.Patch(scimtest.Op("replace", "", map[string]any{"active": false}))); r.Status != 200 || r.Active() {
		t.Fatalf("비활성화: %d %s", r.Status, r.Raw)
	}
	if s.events("leave", adopted.ID()) != 1 {
		t.Error("퇴사 발령이 없음")
	}
	assertChainOK(t, s)
}

// 규격형: RFC 7644 의 오류 경로와 Discovery 표.
func TestSCIMConformanceSpec(t *testing.T) {
	s := newSCIMEnv(t, nil)
	s.enableMode("off")
	c := s.client(s.token("other", "users_groups"))
	u := c.Post("/Users", scimtest.User("spec.user@corp.test", "spec-1", "규격 사용자", "T-PU1", "사원"))
	if u.Status != 201 {
		t.Fatalf("생성: %d %s", u.Status, u.Raw)
	}
	big := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"big@corp.test","displayName":"` + strings.Repeat("가", 400000) + `"}`
	rows := []struct {
		name   string
		resp   scimtest.Resp
		status int
		typ    string
	}{
		{"토큰 없음", (&scimtest.Client{T: t, Base: s.base}).Get("/Users"), 401, ""},
		{"없는 사용자", c.Get("/Users/999999"), 404, ""},
		{"깨진 JSON", c.Post("/Users", "{"), 400, "invalidSyntax"},
		{"필터 문법", c.Get("/Users" + scimtest.Filter(`userName eq "a" or userName eq "b"`)), 400, "invalidFilter"},
		{"잘못된 PATCH 경로", c.Patch("/Users/"+u.ID(), scimtest.Patch(scimtest.Op("replace", "no.such", "x"))), 400, "invalidPath"},
		{"대상 없는 remove", c.Patch("/Users/"+u.ID(), scimtest.Patch(scimtest.Op("remove", "", nil))), 400, "noTarget"},
		{"빈 Operations", c.Patch("/Users/"+u.ID(), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[]}`), 400, "invalidSyntax"},
		{"값 오류(이메일)", c.Post("/Users", scimtest.User("not-an-email", "x", "가", "T-PU1", "사원")), 400, "invalidValue"},
		{"바꿀 수 없는 속성(userName)", c.Patch("/Users/"+u.ID(), scimtest.Patch(scimtest.Op("replace", "userName", "other@corp.test"))), 400, "mutability"},
		{"제거할 수 없는 속성", c.Patch("/Users/"+u.ID(), scimtest.Patch(scimtest.Op("remove", "userName", nil))), 400, "mutability"},
		{"이메일 중복(다른 externalId)", c.Post("/Users", scimtest.User("spec.user@corp.test", "spec-2", "규격 사용자", "T-PU1", "사원")), 409, "uniqueness"},
		{"입사 당일 퇴사", c.Patch("/Users/"+u.ID(), scimtest.Patch(scimtest.Op("replace", "active", false))), 409, ""},
		{"본문 크기 초과", c.Post("/Users", big), 413, "tooMany"},
		{"그룹 꺼짐 쓰기", c.Post("/Groups", group("g", "g")), 403, ""},
	}
	for _, row := range rows {
		if row.resp.Status != row.status || row.resp.ScimType() != row.typ {
			t.Errorf("%s: %d/%q(기대 %d/%q) %s", row.name, row.resp.Status, row.resp.ScimType(), row.status, row.typ, row.resp.Raw)
		}
		if row.status >= 400 {
			if row.resp.Body["status"] != itoa(int64(row.status)) {
				t.Errorf("%s: 오류 본문의 status 가 문자열 %d 가 아님: %v", row.name, row.status, row.resp.Body["status"])
			}
			if schemas, _ := row.resp.Body["schemas"].([]any); len(schemas) != 1 || schemas[0] != "urn:ietf:params:scim:api:messages:2.0:Error" {
				t.Errorf("%s: 오류 schemas: %v", row.name, row.resp.Body["schemas"])
			}
		}
	}
	// Discovery 는 실제 지원과 일치한다(미지원은 false 이고 해당 경로가 없다).
	spc := c.Get("/ServiceProviderConfig")
	for _, k := range []string{"bulk", "sort", "etag", "changePassword"} {
		if m, _ := spc.Body[k].(map[string]any); m["supported"] != false {
			t.Errorf("%s 가 미지원으로 표시되지 않음: %v", k, spc.Body[k])
		}
	}
	if m, _ := spc.Body["patch"].(map[string]any); m["supported"] != true {
		t.Error("patch 지원 표시")
	}
	if r := c.Post("/Bulk", map[string]any{}); r.Status != 404 && r.Status != 405 {
		t.Errorf("/Bulk: %d", r.Status)
	}
	if r := c.Get("/ResourceTypes"); r.Total() != 2 {
		t.Errorf("ResourceTypes: %s", r.Raw)
	}
	if r := c.Get("/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group"); r.Status != 200 {
		t.Errorf("Group 스키마: %d", r.Status)
	}
	// 목록 규격: ListResponse 필드, startIndex 보정(0 → 1), count 상한.
	l := c.Get("/Users?startIndex=0&count=500")
	if l.Status != 200 || l.Body["startIndex"] != float64(1) {
		t.Errorf("startIndex 보정: %s", l.Raw)
	}
	if ipp, _ := l.Body["itemsPerPage"].(float64); ipp > 200 {
		t.Errorf("count 상한: %v", ipp)
	}
	// attributes·excludedAttributes.
	if r := c.Get("/Users/" + u.ID() + "?attributes=userName"); r.Body["userName"] == nil || r.Body["displayName"] != nil || r.Body["id"] == nil {
		t.Errorf("attributes: %s", r.Raw)
	}
	if r := c.Get("/Users/" + u.ID() + "?excludedAttributes=emails"); r.Body["emails"] != nil || r.Body["userName"] == nil {
		t.Errorf("excludedAttributes: %s", r.Raw)
	}
	assertChainOK(t, s)
}
