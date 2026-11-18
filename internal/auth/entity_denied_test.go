package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
)

func (e *pwEnv) deniedRows(t *testing.T) (int, string) {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND result = 'denied'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var diff string
	_ = e.db.QueryRow(`SELECT COALESCE(diff, '') FROM audit_logs WHERE action = 'access.denied' ORDER BY seq DESC LIMIT 1`).Scan(&diff)
	return n, diff
}

// B44 AC-4411: 법인 불일치 접근은 응답은 그대로(403·404)이고 감사 access.denied 로 남는다. 정상 경로는 감사가 늘지 않는다. 감사 값에 상대 법인 정보가 없다.
func TestEntityMismatchAccessIsAudited(t *testing.T) {
	e, _, raw, abc := entityEnv(t)
	whOwn := dbtest.ExecID(t, e.db, `INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-MAIN', '법인1 시험 창고', 1, 't', 't')`)
	whOther := dbtest.ExecID(t, e.db, `INSERT INTO warehouses (code, name, entity_id, created_at, updated_at) VALUES ('WH-ABC', '계열사 창고', ?, 't', 't')`, abc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/warehouses/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		a, _ := contract.ActiveEntityFrom(r.Context())
		var owner int64
		err := e.db.QueryRow(`SELECT entity_id FROM warehouses WHERE id = ?`, id).Scan(&owner)
		if err != nil || (!a.All && owner != a.ID) {
			http.NotFound(w, r) // 서비스가 다른 법인 레코드를 "없음"으로 돌려주는 것을 흉내 낸다
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/probe/{id}", func(w http.ResponseWriter, r *http.Request) {
		// RequireIn 경로: 계열사에서 일하는 사람이 기본 법인의 레코드를 가리키면 404(훅은 요청마다 인증 계층이 싣는다)
		p2 := contract.Principal{UserID: 1, Perms: contract.NewPermSet(contract.PermPurchaseCreate), EntityPerms: map[contract.Permission]contract.EntitySet{contract.PermPurchaseCreate: contract.NewEntitySet(abc)}}
		ctx := contract.WithActiveEntity(contract.WithPrincipal(r.Context(), p2), contract.ActiveEntity{ID: abc})
		if err := contract.RequireIn(ctx, contract.PermPurchaseCreate, contract.DefaultEntityID); err != nil {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := e.s.Wrap(mux)
	do := func(path, header string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
		if header != "" {
			req.Header.Set(entityHeader, header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	before, _ := e.deniedRows(t)

	// 정상 경로: 자기 법인(미배치라 기본 법인)의 창고 200, 아예 없는 번호 404 — 감사가 늘지 않는다.
	if c := do("/api/warehouses/"+strconv.FormatInt(whOwn, 10), ""); c != 200 {
		t.Fatalf("자기 법인 창고: %d", c)
	}
	if c := do("/api/warehouses/9999", ""); c != 404 {
		t.Fatalf("없는 창고: %d", c)
	}
	if n, _ := e.deniedRows(t); n != before {
		t.Fatalf("정상·진짜 404 에서 감사가 늘었다: %d → %d", before, n)
	}

	// 헤더 3종: 고를 수 없는 법인 코드, 모르는 코드, 권한 없는 "*" — 모두 403 이고 denied 행이 하나씩 늘어난다.
	for i, h := range []string{"ABC", "NOPE", "*"} {
		if c := do("/api/warehouses/"+strconv.FormatInt(whOwn, 10), h); c != http.StatusForbidden {
			t.Fatalf("헤더 %q: %d(403 이어야 한다)", h, c)
		}
		if n, _ := e.deniedRows(t); n != before+i+1 {
			t.Fatalf("헤더 %q 감사: %d(기대 %d)", h, n, before+i+1)
		}
	}
	afterHeaders, diff := e.deniedRows(t)
	if !strings.Contains(diff, "현재 법인 지정") || strings.Contains(diff, "계열사") {
		t.Fatalf("헤더 거부 diff: %s", diff)
	}

	// ID 직접 조회: 다른 법인의 창고는 404(존재를 숨김)이고 denied 가 한 번 남는다. diff 에 상대 법인 코드·이름이 없다.
	if c := do("/api/warehouses/"+strconv.FormatInt(whOther, 10), ""); c != 404 {
		t.Fatalf("다른 법인 창고: %d", c)
	}
	n, diff := e.deniedRows(t)
	if n != afterHeaders+1 {
		t.Fatalf("ID 조회 감사: %d(기대 %d)", n, afterHeaders+1)
	}
	if !strings.Contains(diff, "창고") || !strings.Contains(diff, "다른 법인의 레코드") || strings.Contains(diff, "ABC") || strings.Contains(diff, "계열사") {
		t.Fatalf("ID 조회 diff 에 상대 법인 정보가 있거나 내용이 비었다: %s", diff)
	}

	// RequireIn 경로: 404 이고 한 번만 남는다(404 점검과 겹쳐 두 번 남지 않는다).
	if c := do("/api/probe/1", ""); c != 404 {
		t.Fatalf("RequireIn: %d", c)
	}
	if n2, d2 := e.deniedRows(t); n2 != n+1 || !strings.Contains(d2, "권한: ") {
		t.Fatalf("RequireIn 감사: %d(기대 %d) %s", n2, n+1, d2)
	}
}
