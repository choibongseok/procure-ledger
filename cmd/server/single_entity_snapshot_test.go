package main

// 단일 법인 동작 불변 스냅샷(B44 명세 8.4, AC-4401). 법인이 기본 법인 하나뿐인 설치에서 읽기 라우트(경로 변수 없는 GET)가 기본 역할별로 내는
// 상태 코드와 응답 모양(키 구조, 값은 보지 않는다)을 골든으로 잡는다. 1단계(법인 기반)는 업무 코드를 바꾸지 않으므로 이 골든을 1단계 머지 시점에 만들고,
// 2단계(업무 데이터 법인 축)가 저장소·서비스를 바꾼 뒤에도 같아야 한다. 다시 만들려면 UPDATE_SNAPSHOT=1(동작이 의도적으로 바뀐 경우만, 사유를 명세에 적는다).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const snapshotPath = "testdata/single_entity_snapshot.golden.json"

// shapeOf 는 JSON 값의 모양이다: 객체는 키 이름순, 배열은 첫 원소의 모양(비면 "[]"), 나머지는 형.
func shapeOf(v any, depth int) string {
	switch x := v.(type) {
	case map[string]any:
		if depth == 0 {
			return "{…}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ":" + shapeOf(x[k], depth-1)
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(x) == 0 {
			return "[]"
		}
		return "[" + shapeOf(x[0], depth-1) + "]"
	case string:
		return "s"
	case float64:
		return "n"
	case bool:
		return "b"
	case nil:
		return "null"
	}
	return "?"
}

func TestSingleEntityBehaviorSnapshot(t *testing.T) {
	rt, db, _ := fullEnv(t)
	ctx := context.Background()
	h := rt.Handler()
	users := map[string]string{"admin": "admin@pl.test", "finance": "finance@pl.test", "staff": "staff@pl.test", "auditor": "auditor@pl.test"}
	principals := map[string]contract.Principal{}
	for role, email := range users {
		var p contract.Principal
		var r string
		if err := db.QueryRowContext(ctx, `SELECT id, name, role, department_id FROM users WHERE email = ?`, email).Scan(&p.UserID, &p.Name, &r, &p.DepartmentID); err != nil {
			t.Fatal(err)
		}
		p.Role, p.Channel, p.Auth = contract.Role(r), contract.ChannelWeb, contract.AuthSession
		var err error
		if p.Perms, err = access.PermsOf(ctx, db, p.UserID); err != nil {
			t.Fatal(err)
		}
		_, own, err := org.EntitiesOf(ctx, db, p.UserID, contract.DayOf(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		if len(own) == 0 {
			own = []int64{contract.DefaultEntityID}
		}
		p.OwnEntities = own
		if p.EntityPerms, err = access.EntityPermsOf(ctx, db, p.UserID, own); err != nil {
			t.Fatal(err)
		}
		principals[role] = p
	}
	var paths []string
	for _, r := range rt.Routes() {
		if r.Method != http.MethodGet || strings.Contains(r.Pattern, "{") || !strings.HasPrefix(r.Pattern, "/api/") {
			continue
		}
		if strings.HasPrefix(r.Pattern, "/api/auth/") || strings.HasPrefix(r.Pattern, "/api/setup") || strings.Contains(r.Pattern, "/export") || strings.Contains(r.Pattern, "export-") {
			continue
		}
		paths = append(paths, r.Pattern)
	}
	sort.Strings(paths)
	got := map[string]string{}
	for _, role := range []string{"admin", "finance", "staff", "auditor"} {
		p := principals[role]
		for _, path := range paths {
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(contract.WithActiveEntity(contract.WithPrincipal(ctx, p), contract.ActiveEntity{ID: contract.DefaultEntityID}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			shape := ""
			if strings.Contains(w.Header().Get("Content-Type"), "json") && w.Code == 200 {
				var v any
				if err := json.Unmarshal(w.Body.Bytes(), &v); err == nil {
					shape = shapeOf(v, 3)
				}
			}
			got[role+" GET "+path] = http.StatusText(w.Code) + " " + shape
		}
	}
	if len(got) < 100 {
		t.Fatalf("스냅샷이 너무 작다: %d", len(got))
	}
	if os.Getenv("UPDATE_SNAPSHOT") == "1" {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(snapshotPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("골든이 없습니다(UPDATE_SNAPSHOT=1 로 만드세요): %v", err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var diffs []string
	for k, w := range want {
		if g, ok := got[k]; !ok {
			diffs = append(diffs, "사라짐: "+k)
		} else if g != w {
			diffs = append(diffs, "달라짐: "+k+"\n    기대 "+w+"\n    실제 "+g)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diffs = append(diffs, "새로 생김: "+k)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		if len(diffs) > 25 {
			diffs = append(diffs[:25], "…")
		}
		t.Fatalf("단일 법인 동작이 이전과 다릅니다(%d곳):\n  %s", len(diffs), strings.Join(diffs, "\n  "))
	}
}
