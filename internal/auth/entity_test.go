package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

// B44 10절(AC-4410·4430): 요청마다 소속·겸직 법인과 권한별 효력 법인을 읽고, 현재 법인(헤더·세션 값)은 고를 수 있는 법인 안일 때만 허용한다.

func entityEnv(t *testing.T) (*pwEnv, contract.Principal, string, int64) {
	t.Helper()
	e := newPwEnv(t, false)
	ctx := context.Background()
	if _, err := access.SyncTemplates(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, err := e.login("staff@pl.test", goodPw)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := e.s.sessionPrincipal(ctx, lr.RawSession)
	if err != nil {
		t.Fatal(err)
	}
	id := dbtest.ExecID(t, e.db, `INSERT INTO entities (code, name, created_at, updated_at) VALUES ('ABC', '계열사', 't', 't')`)
	return e, p, lr.RawSession, id
}

func TestSingleEntityInstallKeepsOldBehavior(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	if _, err := access.SyncTemplates(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, err := e.login("staff@pl.test", goodPw)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := e.s.sessionPrincipal(ctx, lr.RawSession)
	if err != nil || p.EntityPerms == nil || len(p.OwnEntities) != 1 || p.OwnEntities[0] != contract.DefaultEntityID {
		t.Fatalf("법인이 하나뿐이면 소속은 기본 법인: %+v %v", p.OwnEntities, err)
	}
	if !p.CanIn(contract.PermPurchaseCreate, contract.DefaultEntityID) || p.CanIn(contract.PermPurchaseCreate, 2) {
		t.Fatal("기본 법인에서만 권한")
	}
	r := httptest.NewRequest("GET", "/api/me", nil)
	a, err := e.s.resolveEntity(ctx, p, r, 0)
	if err != nil || a.ID != contract.DefaultEntityID || a.All {
		t.Fatalf("현재 법인: %+v %v", a, err)
	}
	// /api/me 에는 법인이 하나만 실린다(화면은 선택기를 그리지 않는다).
	me := e.s.meEntities(contract.WithActiveEntity(ctx, a), p)
	if list := me["entities"].([]entityInfo); len(list) != 1 || me["groupScope"].(bool) {
		t.Fatalf("me: %+v", me)
	}
}

func TestResolveEntityHeaderSessionAndDenial(t *testing.T) {
	e, p, raw, abc := entityEnv(t)
	ctx := context.Background()
	req := func(h string) *http.Request {
		r := httptest.NewRequest("GET", "/api/x", nil)
		if h != "" {
			r.Header.Set(entityHeader, h)
		}
		return r
	}
	// 법인이 둘이 되면 조직에 배치되지 않은 사람의 own 은 효력이 없다(명세 E2). 그래서 고를 수 있는 법인도 없다.
	p, _, err := e.s.sessionPrincipal(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.OwnEntities) != 0 {
		t.Fatalf("미배치: %v", p.OwnEntities)
	}
	if _, err := e.s.resolveEntity(ctx, p, req("ABC"), 0); !errors.Is(err, errEntityDenied) {
		t.Fatalf("범위 밖 헤더: %v", err)
	}
	if _, err := e.s.resolveEntity(ctx, p, req("*"), 0); !errors.Is(err, errEntityDenied) {
		t.Fatalf("그룹 범위 없이 전체: %v", err)
	}
	if a, err := e.s.resolveEntity(ctx, p, req(""), 0); err != nil || a.ID != contract.DefaultEntityID {
		t.Fatalf("헤더·세션 값이 없으면 기본 법인: %+v %v", a, err)
	}
	// ABC 법인 범위로 역할을 부여하면 ABC 를 고를 수 있다.
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id)
		SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = 'finance'`, p.UserID, abc); err != nil {
		t.Fatal(err)
	}
	p, _, _ = e.s.sessionPrincipal(ctx, raw)
	if !p.CanIn(contract.PermLedgerWrite, abc) || p.CanIn(contract.PermLedgerWrite, contract.DefaultEntityID) {
		t.Fatalf("법인 범위 권한: %+v", p.EntityPerms[contract.PermLedgerWrite])
	}
	a, err := e.s.resolveEntity(ctx, p, req("abc"), 0) // 코드는 대소문자를 가리지 않는다
	if err != nil || a.ID != abc {
		t.Fatalf("헤더: %+v %v", a, err)
	}
	if a, err = e.s.resolveEntity(ctx, p, req(""), abc); err != nil || a.ID != abc {
		t.Fatalf("세션 값: %+v %v", a, err)
	}
	if a, err = e.s.resolveEntity(ctx, p, req("ABC"), 999); err != nil || a.ID != abc {
		t.Fatalf("헤더가 세션 값보다 우선: %+v %v", a, err)
	}
	// 세션에 정했던 법인이 범위 밖이 되면 기본 법인으로 되돌린다(E14).
	if a, err = e.s.resolveEntity(ctx, p, req(""), 77); err != nil || a.ID != contract.DefaultEntityID {
		t.Fatalf("범위 밖 세션 값: %+v %v", a, err)
	}
	// 그룹 범위 부여가 있으면 전체 모드가 된다.
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope)
		SELECT id, 'user', ?, 'manual', 't', 'group' FROM roles WHERE key = 'auditor'`, p.UserID); err != nil {
		t.Fatal(err)
	}
	p, _, _ = e.s.sessionPrincipal(ctx, raw)
	if a, err = e.s.resolveEntity(ctx, p, req("*"), 0); err != nil || !a.All {
		t.Fatalf("전체 모드: %+v %v", a, err)
	}
	if a, err = e.s.resolveEntity(ctx, p, req(""), sessionAll); err != nil || !a.All {
		t.Fatalf("세션의 전체 모드: %+v %v", a, err)
	}
}

func TestSetEntityHandlerStoresSessionValue(t *testing.T) {
	e, _, raw, abc := entityEnv(t)
	ctx := context.Background()
	p0, _, _ := e.s.sessionPrincipal(ctx, raw)
	if _, err := e.db.Exec(`INSERT INTO role_grants (role_id, target_type, target_id, source, created_at, scope, scope_entity_id)
		SELECT id, 'user', ?, 'manual', 't', 'entity', ? FROM roles WHERE key = 'finance'`, p0.UserID, abc); err != nil {
		t.Fatal(err)
	}
	p, _, _ := e.s.sessionPrincipal(ctx, raw)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/me/entity", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
		r = r.WithContext(contract.WithPrincipal(ctx, p))
		w := httptest.NewRecorder()
		e.s.handleSetEntity(w, r)
		return w
	}
	if w := call(`{"entity":"ABC"}`); w.Code != 200 || e.s.sessionEntity(ctx, raw) != abc {
		t.Fatalf("법인 바꾸기: %d %s", w.Code, w.Body.String())
	}
	if w := call(`{"entity":"NOPE"}`); w.Code != http.StatusForbidden {
		t.Fatalf("없는 법인: %d", w.Code)
	}
	if w := call(`{"entity":"*"}`); w.Code != http.StatusForbidden {
		t.Fatalf("그룹 범위 없는 전체: %d", w.Code)
	}
	if e.s.sessionEntity(ctx, raw) != abc {
		t.Fatal("거부된 요청이 세션 값을 바꿈")
	}
	if w := call(`not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("형식 오류: %d", w.Code)
	}
}
