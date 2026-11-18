package mcptool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// fakeDrafts 는 서비스를 부르지 않는 초안 대상이다(호출 기록만 남긴다).
func fakeDraftCatalog() (*mcptool.Catalog, *[]map[string]any) {
	cat, _ := newFake()
	var created []map[string]any
	cat.WithDrafts(&mcptool.DraftTarget{
		Name: "pr_draft", Label: "구매요청 초안", Perm: contract.PermPurchaseCreate,
		Fields: []mcptool.DraftField{
			{Name: "title", Label: "제목", Type: mcptool.TypeString, Required: true},
			{Name: "quantity", Label: "수량", Type: mcptool.TypeInt, Required: true},
			{Name: "due_date", Label: "납기", Type: mcptool.TypeDate},
		},
		Create: func(_ context.Context, v map[string]any) (map[string]any, error) {
			created = append(created, v)
			return map[string]any{"no": "PR-1", "status": "draft"}, nil
		},
	})
	return cat, &created
}

func draftDef() map[string]any {
	return map[string]any{
		"format": "pl-mcp-tool/1", "name": "custom_make_pr", "kind": "draft", "description": "구매요청 초안을 만든다.", "target": "pr_draft",
		"input": map[string]any{"title": map[string]any{"type": "string", "required": true}, "qty": map[string]any{"type": "int", "min": 1, "max": 100, "required": true}},
		"map": map[string]any{
			"title":    map[string]any{"from": "title"},
			"quantity": map[string]any{"from": "qty"},
			"due_date": map[string]any{"value": "2026-12-31"},
		},
	}
}

func (e *env) drafty(t *testing.T) (*mcptool.Service, *[]map[string]any) {
	t.Helper()
	cat, created := fakeDraftCatalog()
	return e.mk(cat), created
}

// AC-4807: 초안 정의 검증 — 초안 종류 2종만, 맵은 허용 칸만, 결재선·상태·승인·법인 칸 거부, 필수 칸 누락·종류 불일치 거부.
func TestDraftDefinitionValidation(t *testing.T) {
	cat, _ := fakeDraftCatalog()
	mut := func(f func(m map[string]any)) []byte {
		m := draftDef()
		f(m)
		return raw(m)
	}
	if _, verr := mcptool.ParseAndValidate(raw(draftDef()), cat); verr != nil {
		t.Fatalf("기본 초안 정의가 거부됨: %+v", verr.Errors)
	}
	cases := []struct {
		name, path string
		raw        []byte
	}{
		{"초안 종류 모름", "target", mut(func(m map[string]any) { m["target"] = "approve_all" })},
		{"결재선 칸", "map.approval_line", mut(func(m map[string]any) { m["map"].(map[string]any)["approval_line"] = map[string]any{"value": "x"} })},
		{"상태 칸", "map.status", mut(func(m map[string]any) { m["map"].(map[string]any)["status"] = map[string]any{"value": "approved"} })},
		{"법인 칸", "map.entity", mut(func(m map[string]any) { m["map"].(map[string]any)["entity"] = map[string]any{"value": "ABC"} })},
		{"승인자 칸", "map.approver", mut(func(m map[string]any) { m["map"].(map[string]any)["approver"] = map[string]any{"value": "1"} })},
		{"필수 칸 누락", "map.quantity", mut(func(m map[string]any) { delete(m["map"].(map[string]any), "quantity") })},
		{"입력 종류 불일치", "map.quantity.from", mut(func(m map[string]any) { m["map"].(map[string]any)["quantity"] = map[string]any{"from": "title"} })},
		{"입력 이름 모름", "map.title.from", mut(func(m map[string]any) { m["map"].(map[string]any)["title"] = map[string]any{"from": "nope"} })},
		{"값과 입력 둘 다", "map.title", mut(func(m map[string]any) {
			m["map"].(map[string]any)["title"] = map[string]any{"from": "title", "value": "x"}
		})},
		{"상수 형식", "map.due_date.value", mut(func(m map[string]any) { m["map"].(map[string]any)["due_date"] = map[string]any{"value": "내일"} })},
		{"맥락 값 불가", "map.title.value", mut(func(m map[string]any) { m["map"].(map[string]any)["title"] = map[string]any{"value": "@my_user"} })},
		{"예약 입력 이름", "input.idempotency_key", mut(func(m map[string]any) {
			m["input"].(map[string]any)["idempotency_key"] = map[string]any{"type": "string"}
		})},
		{"자원 섞임", "kind", mut(func(m map[string]any) { m["resource"] = "things" })},
		{"조회에 맵", "kind", func() []byte { m := baseDef(); m["map"] = map[string]any{}; m["target"] = "pr_draft"; return raw(m) }()},
	}
	for _, c := range cases {
		_, verr := mcptool.ParseAndValidate(c.raw, cat)
		if verr == nil {
			t.Errorf("%s: 거부되어야 하는데 통과함", c.name)
			continue
		}
		found := false
		for _, e := range verr.Errors {
			if strings.Contains(e.Path, c.path) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: 기대한 경로 %q 의 오류가 없음: %+v", c.name, c.path, verr.Errors)
		}
	}
	// 카탈로그의 실제 초안 대상에는 금지 칸 이름이 없다.
	for _, tgt := range mcptool.BuiltinCatalog(depsEmpty()).Drafts() {
		for _, f := range tgt.Fields {
			for _, bad := range mcptool.ForbiddenMapFields {
				if f.Name == bad {
					t.Errorf("%s.%s: 맵에 쓸 수 없는 이름의 칸", tgt.Name, f.Name)
				}
			}
		}
	}
	if n := len(mcptool.BuiltinCatalog(depsEmpty()).Drafts()); n != 2 {
		t.Fatalf("초안 대상 %d개(2개여야 함)", n)
	}
}

// 초안 도구: 저장은 낮은 기본 한도(5)로, 시험 실행은 초안을 만들지 않고 칸 미리보기만 보이며, 직접 켤 수 없다.
func TestDraftToolSaveTestAndNoDirectEnable(t *testing.T) {
	e := newEnv(t)
	svc, created := e.drafty(t)
	d, err := svc.Save(manage, mcptool.SaveInput{Title: "구매요청 만들기", Definition: raw(draftDef())})
	if err != nil || d.Kind != "draft" || d.RatePerMin != 5 || d.ResourceLabel != "구매요청 초안" || d.Resource != "pr_draft" {
		t.Fatalf("초안 도구 저장: %+v %v", d.ToolView, err)
	}
	out, err := svc.Test(manage, mcptool.TestInput{Definition: raw(draftDef()), Input: map[string]any{"title": "복사용지", "qty": 3}})
	if err != nil || out.Count != 1 || len(*created) != 0 || !strings.Contains(out.Notice, "초안을 만들지 않습니다") {
		t.Fatalf("시험 실행: %+v 생성 %d %v", out, len(*created), err)
	}
	row := out.Rows[0]
	if row["title"] != "복사용지" || row["quantity"] != int64(3) || row["due_date"] != "2026-12-31" || len(out.CallableRoles) == 0 {
		t.Fatalf("미리보기: %+v %v", row, out.CallableRoles)
	}
	if _, err := svc.Test(manage, mcptool.TestInput{Definition: raw(draftDef()), Input: map[string]any{"title": "x"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("필수 입력 누락: %v", err)
	}
	if got, _ := svc.Get(manage, d.ID); !got.Tested {
		t.Fatal("시험 통과 표시가 없음")
	}
	if _, err := svc.SetEnabled(manage, d.ID, true); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "다른 관리자의 확인") {
		t.Fatalf("초안 도구를 직접 켬: %v", err)
	}
	// 도구 종류는 바꿀 수 없다.
	rd := baseDef()
	rd["name"] = "custom_make_pr"
	if _, err := svc.Save(manage, mcptool.SaveInput{ID: d.ID, Title: "x", Definition: raw(rd)}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("종류 변경: %v", err)
	}
}

func as2(perms ...contract.Permission) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 2, Role: contract.RoleAdmin, DepartmentID: 900, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(perms...)})
}

// AC-4808: 초안 도구는 요청자 ≠ 확인자일 때만 켜지고, 정의가 바뀌면 진행 요청이 취소되며 켜져 있던 도구는 꺼진다.
func TestDraftTwoPersonEnable(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (2, 'admin2@pl.test', '관리자2', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	svc, _ := e.drafty(t)
	admin1, admin2 := manage, as2(contract.PermMCPToolRead, contract.PermMCPToolManage)
	d, err := svc.Save(admin1, mcptool.SaveInput{Title: "구매요청 만들기", Definition: raw(draftDef())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestEnable(admin1, d.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "시험 실행") {
		t.Fatalf("시험 없이 요청: %v", err)
	}
	if _, err := svc.Test(admin1, mcptool.TestInput{Definition: raw(draftDef()), Input: map[string]any{"title": "a", "qty": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestEnable(reader, d.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 요청: %v", err)
	}
	r, err := svc.RequestEnable(admin1, d.ID)
	if err != nil || r.Pending == nil || r.Pending.Version != 1 || r.Pending.RequestedBy != "관리자" {
		t.Fatalf("요청: %+v %v", r.Pending, err)
	}
	if _, err := svc.RequestEnable(admin2, d.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("진행 중 요청이 있는데 또 요청: %v", err)
	}
	if _, err := svc.ConfirmEnable(admin1, d.ID, mcptool.EnableOpts{}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "직접 확인할 수 없습니다") {
		t.Fatalf("요청자가 직접 확인: %v", err)
	}
	if _, err := svc.ConfirmEnable(reader, d.ID, mcptool.EnableOpts{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 확인: %v", err)
	}
	// DB 검사도 같은 사람의 확인을 막는다.
	if _, err := e.db.Exec(`UPDATE mcp_tool_enable_requests SET confirmed_by = requested_by, status = 'confirmed'`); err == nil {
		t.Fatal("DB 가 같은 사람의 확인을 막지 못함")
	}
	on, err := svc.ConfirmEnable(admin2, d.ID, mcptool.EnableOpts{})
	if err != nil || !on.Enabled || on.Pending != nil {
		t.Fatalf("확인: %+v %v", on.ToolView, err)
	}
	for _, a := range []string{"mcptool.enable_request", "mcptool.enable_confirm"} {
		if !e.rec.Has(a, "mcp_tool", "success") {
			t.Fatalf("%s 감사가 없음", a)
		}
	}
	// 켜진 초안 도구의 새 버전은 도구를 끈다(다시 확인받아야 한다).
	m2 := draftDef()
	m2["map"].(map[string]any)["due_date"] = map[string]any{"value": "2027-01-31"}
	nv, err := svc.Save(admin1, mcptool.SaveInput{ID: d.ID, Title: "구매요청 만들기", Definition: raw(m2)})
	if err != nil || nv.Enabled || nv.CurrentVersion != 2 {
		t.Fatalf("새 버전은 꺼짐이어야 함: %+v %v", nv.ToolView, err)
	}
	// 진행 중 요청은 정의가 바뀌면 취소된다. 취소·반려는 한 번에 하나만 끝난다.
	if _, err := svc.Test(admin1, mcptool.TestInput{Definition: raw(m2), Input: map[string]any{"title": "a", "qty": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestEnable(admin1, d.ID); err != nil {
		t.Fatal(err)
	}
	m3 := draftDef()
	m3["map"].(map[string]any)["due_date"] = map[string]any{"value": "2027-02-28"}
	if got, err := svc.Save(admin1, mcptool.SaveInput{ID: d.ID, Title: "구매요청 만들기", Definition: raw(m3)}); err != nil || got.Pending != nil {
		t.Fatalf("정의 변경 뒤 진행 요청: %+v %v", got.Pending, err)
	}
	if _, err := svc.ConfirmEnable(admin2, d.ID, mcptool.EnableOpts{}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("취소된 요청 확인: %v", err)
	}
	// 요청자는 취소, 다른 관리자는 반려한다.
	if _, err := svc.Test(admin1, mcptool.TestInput{Definition: raw(m3), Input: map[string]any{"title": "a", "qty": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestEnable(admin1, d.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := svc.CancelEnable(admin2, d.ID); err != nil || c.Pending != nil {
		t.Fatalf("반려: %+v %v", c.Pending, err)
	}
	var st string
	_ = e.db.QueryRow(`SELECT status FROM mcp_tool_enable_requests ORDER BY id DESC LIMIT 1`).Scan(&st)
	if st != "rejected" {
		t.Fatalf("상태 %s", st)
	}
	if _, err := svc.CancelEnable(admin1, d.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("없는 요청 취소: %v", err)
	}
	// 조회 도구에는 켜기 요청이 없다.
	rd := e.save(baseDef(), 0)
	if _, err := e.svc.RequestEnable(admin1, rd.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("조회 도구 요청: %v", err)
	}
}

type fakeEval struct {
	view evalset.RunView
	err  error
	n    int
}

func (f *fakeEval) RunSync(context.Context, contract.Principal, string, evalset.RunInput) (evalset.RunView, error) {
	f.n++
	return f.view, f.err
}

func (e *env) withEval(f *fakeEval) *mcptool.Service {
	svc, err := mcptool.New(mcptool.Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Catalog: e.cat, Masker: rbac.Masker{}, Eval: f,
		Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

// AC-4814: 연결된 tool 세트가 합격해야 켜지고, 미달이면 사유와 함께 확인해야 켜지며 감사에 사유 유무가 남는다.
func TestEvalLinkGatesEnable(t *testing.T) {
	e := newEnv(t)
	fe := &fakeEval{view: evalset.RunView{Status: evalset.StatusDone, GatePassed: true, PassRatePct: 95, ThresholdPct: 90}}
	svc := e.withEval(fe)
	d := e.save(baseDef(), 0)
	if _, err := e.db.Exec(`INSERT INTO eval_sets (code, name, purpose, created_at, updated_at) VALUES ('tools-eval', '도구 평가', 'tool', 't', 't'), ('text-eval', '문장', 'text', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetEvalLink(manage, d.ID, "nope"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("없는 세트: %v", err)
	}
	if _, err := svc.SetEvalLink(manage, d.ID, "text-eval"); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("용도가 다른 세트: %v", err)
	}
	if _, err := svc.SetEvalLink(reader, d.ID, "tools-eval"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한: %v", err)
	}
	l, err := svc.SetEvalLink(manage, d.ID, "tools-eval")
	if err != nil || l.EvalSetID == 0 {
		t.Fatalf("연결: %+v %v", l.ToolView, err)
	}
	if _, err := svc.SetEvalLink(manage, d.ID, "tools-eval"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 연결: %v", err)
	}
	if _, err := svc.Test(manage, mcptool.TestInput{Definition: raw(baseDef()), Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	// 합격: 그냥 켜진다(평가를 한 번 돌렸다).
	if on, err := svc.SetEnabled(manage, d.ID, true); err != nil || !on.Enabled || fe.n != 1 {
		t.Fatalf("합격: %+v 실행 %d %v", on.ToolView, fe.n, err)
	}
	if _, err := svc.SetEnabled(manage, d.ID, false); err != nil {
		t.Fatal(err)
	}
	// 미달: 확인 없이는 거부(409 상당 EvalGateError), 사유 없는 확인도 거부, 사유와 함께 확인하면 켜지고 감사.
	fe.view = evalset.RunView{Status: evalset.StatusDone, GatePassed: false, PassRatePct: 60, ThresholdPct: 90, CriticalFails: 1}
	_, err = svc.SetEnabled(manage, d.ID, true)
	var eg *mcptool.EvalGateError
	if !errors.As(err, &eg) || eg.Result.PassRatePct != 60 || eg.Result.SetCode != "tools-eval" || !strings.Contains(eg.Result.Message, "기준 미달") {
		t.Fatalf("미달: %+v %v", eg, err)
	}
	if _, err := svc.SetEnabled(manage, d.ID, true, mcptool.EnableOpts{EvalOverride: true}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 확인: %v", err)
	}
	if got, _ := svc.Get(manage, d.ID); got.Enabled {
		t.Fatal("거부했는데 켜짐")
	}
	if on, err := svc.SetEnabled(manage, d.ID, true, mcptool.EnableOpts{EvalOverride: true, Reason: "임시로 켠다"}); err != nil || !on.Enabled {
		t.Fatalf("확인 후 켜기: %+v %v", on.ToolView, err)
	}
	var given []string
	for _, r := range e.rec.Records() {
		if r.Action != "mcptool.eval_override" {
			continue
		}
		keys := map[string]string{}
		for _, df := range r.Diff {
			keys[df.Key] = df.New
			if strings.Contains(df.New, "임시로") {
				t.Fatalf("감사에 사유 원문이 있음: %+v", df)
			}
		}
		if keys["set_code"] != "tools-eval" || keys["pass_rate_pct"] != "60" {
			t.Fatalf("무시 감사 diff: %+v", r.Diff)
		}
		given = append(given, keys["reason_given"])
	}
	if len(given) != 1 || given[0] != "있음" {
		t.Fatalf("사유 유무: %v", given)
	}
	// 평가를 못 돌려도(실행 오류) 미달과 같이 확인을 요구한다. 해제하면 평가 없이 켜진다.
	if _, err := svc.SetEnabled(manage, d.ID, false); err != nil {
		t.Fatal(err)
	}
	fe.err = errors.New("boom")
	if _, err := svc.SetEnabled(manage, d.ID, true); !errors.As(err, &eg) || !strings.Contains(eg.Result.Message, "실행하지 못했습니다") {
		t.Fatalf("실행 오류: %v", err)
	}
	if _, err := svc.SetEvalLink(manage, d.ID, ""); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.SetEnabled(manage, d.ID, true); err != nil || !on.Enabled {
		t.Fatalf("연결 해제 뒤 켜기: %v", err)
	}
	if !e.rec.Has("mcptool.eval_link", "mcp_tool", "success") {
		t.Fatal("mcptool.eval_link 감사가 없음")
	}
}

// AC-4813: 내보내기·가져오기 — 왕복이 정의를 보존하고, 검증 실패 도구만 거부(사유 한국어), 통과분은 꺼진 새 도구, 이름 충돌은 거부, 번호·사용자·비밀이 파일에 없다.
func TestExportImportRoundTrip(t *testing.T) {
	e := newEnv(t)
	svc, _ := e.drafty(t)
	a, err := svc.Save(manage, mcptool.SaveInput{Title: "열린 항목", Definition: raw(baseDef())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(manage, mcptool.SaveInput{Title: "초안", Definition: raw(draftDef())}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Export(reader); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한 내보내기: %v", err)
	}
	out, err := svc.Export(manage)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Format string            `json:"format"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(out, &file); err != nil || file.Format != "pl-mcp-tools/1" || len(file.Tools) != 2 {
		t.Fatalf("내보내기: %s %v", out, err)
	}
	for _, bad := range []string{"\"id\"", "users", "admin@", "secret", "created_by", "enabled", "열린 항목", "초안\""} {
		if strings.Contains(string(out), bad) {
			t.Fatalf("파일에 %q 가 있음", bad)
		}
	}
	// 같은 DB 에 다시 가져오면 둘 다 이름 충돌로 거부된다(덮어쓰기 없음).
	res, err := svc.Import(manage, out)
	if err != nil || res.Imported != 0 || res.Rejected != 2 || !strings.Contains(res.Items[0].Message, "이름 충돌") {
		t.Fatalf("충돌 가져오기: %+v %v", res, err)
	}
	// 다른 설치(빈 DB)로 가져오면 정의가 보존되고 꺼진 도구가 된다. 하나가 검증 실패여도 나머지는 통과한다.
	e2 := newEnv(t)
	svc2, _ := e2.drafty(t)
	var bad map[string]any
	_ = json.Unmarshal(file.Tools[1], &bad)
	bad["name"] = "custom_bad_import"
	bad["resource"] = "users"
	badRaw, _ := json.Marshal(bad)
	mixed, _ := json.Marshal(map[string]any{"format": "pl-mcp-tools/1", "tools": []json.RawMessage{file.Tools[0], file.Tools[1], badRaw}})
	res, err = svc2.Import(manage, mixed)
	if err != nil || res.Imported != 2 || res.Rejected != 1 {
		t.Fatalf("가져오기: %+v %v", res, err)
	}
	if res.Items[2].OK || !strings.Contains(res.Items[2].Message, "resource") || strings.Contains(res.Items[2].Message, "users") {
		t.Fatalf("거부 사유: %+v", res.Items[2])
	}
	list, _ := svc2.List(manage)
	if len(list) != 2 {
		t.Fatalf("만들어진 도구 %d개", len(list))
	}
	for _, v := range list {
		if v.Enabled || v.CurrentVersion != 1 {
			t.Fatalf("가져온 도구는 꺼진 v1 이어야 함: %+v", v)
		}
	}
	got, _ := svc2.Get(manage, list[0].ID)
	orig, _ := e.svc.Get(manage, a.ID)
	_, wantHash := hashOf(t, orig.Definition)
	_, gotHash := hashOf(t, got.Definition)
	if wantHash != gotHash && list[0].Name == a.Name {
		t.Fatalf("왕복이 정의를 바꿈")
	}
	// 형식·크기·개수 오류.
	for name, body := range map[string][]byte{"형식": []byte(`{"format":"x","tools":[]}`), "빈 목록": []byte(`{"format":"pl-mcp-tools/1","tools":[]}`), "JSON 아님": []byte(`{bad`), "큼": make([]byte, mcptool.MaxImportBytes+1)} {
		if _, err := svc2.Import(manage, body); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := svc2.Import(reader, mixed); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한 가져오기: %v", err)
	}
	for _, a := range []string{"mcptool.export", "mcptool.import"} {
		if !e.rec.Has(a, "mcp_tool", "success") && !e2.rec.Has(a, "mcp_tool", "success") {
			t.Fatalf("%s 감사가 없음", a)
		}
	}
}

func hashOf(t *testing.T, raw json.RawMessage) (mcptool.Definition, string) {
	t.Helper()
	cat, _ := fakeDraftCatalog()
	def, verr := mcptool.ParseAndValidate(raw, cat)
	if verr != nil {
		t.Fatal(verr)
	}
	_, h, _ := mcptool.Canonical(def)
	return def, h
}
