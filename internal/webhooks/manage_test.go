package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/webhooks"
)

func str(s string) *string { return &s }

func TestUpdateSubscriptionKeepsSecretAndHidesURL(t *testing.T) {
	e := newEnv(t)
	c := e.sub("연동 A", nil)
	e.sub("연동 B", nil)
	ctx := e.ctx()
	// 이름·받을 종류·"내가 만든 변경도 받기" 변경.
	inc := true
	v, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{Name: str("연동 A2"), Events: []string{"item.updated"}, IncludeAPI: &inc})
	if err != nil || v.Name != "연동 A2" || len(v.Events) != 1 || !v.IncludeAPI {
		t.Fatalf("수정: %+v %v", v, err)
	}
	if _, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{Name: str("연동 B")}); err == nil || !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("이름 중복: %v", err)
	}
	if _, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{Events: []string{"nope.created"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 종류: %v", err)
	}
	if _, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{Events: []string{}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 종류: %v", err)
	}
	if _, err := e.svc.Update(ctx, 9999, webhooks.UpdateInput{Name: str("없음")}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 구독: %v", err)
	}
	// 주소 변경: 서명 비밀은 그대로여서 새 주소로 간 요청도 원래 비밀로 검증된다.
	r2 := &recv{}
	second := httptest.NewServer(r2.handler())
	defer second.Close()
	if _, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{URL: str("http://example.invalid/x")}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("해석되지 않는 주소는 거부: %v", err)
	}
	v, err = e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{URL: str(second.URL + "/moved")})
	if err != nil || !strings.Contains(v.Hint, "127.0.0.1") {
		t.Fatalf("주소 변경: %+v %v", v, err)
	}
	e.note(changes.ResItem, 5, changes.OpUpdated, "")
	e.tick()
	reqs := r2.all()
	if len(reqs) != 1 {
		t.Fatalf("새 주소 수신 %d건", len(reqs))
	}
	r := reqs[0]
	ts, _ := strconv.ParseInt(r.hdr.Get("X-PL-Timestamp"), 10, 64)
	if want := "v1=" + webhooks.Sign(c.Secret, ts, r.body); !strings.Contains(r.hdr.Get("X-PL-Signature"), want) {
		t.Fatal("주소를 바꿔도 서명 비밀은 그대로여야 한다")
	}
	// 감사에는 주소가 어떤 형태로도 없고 바뀐 항목만 있다.
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.sub_save' AND (diff LIKE '%http%' OR detail LIKE '%http%' OR diff LIKE '%/moved%')`) != 0 {
		t.Fatal("감사에 주소가 있다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.sub_save' AND diff LIKE '%url_changed%'`) != 1 {
		t.Fatal("주소 변경 표시(url_changed)가 한 건이어야 한다")
	}
	// 바꿀 것이 없으면 감사도 남기지 않는다.
	before := e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.sub_save'`)
	if _, err := e.svc.Update(ctx, c.View.ID, webhooks.UpdateInput{}); err != nil || e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.sub_save'`) != before {
		t.Fatalf("빈 수정: %v", err)
	}
}

func TestResendFailedBulkRespectsPerDeliveryLimit(t *testing.T) {
	e := newEnv(t)
	e.recv.status = func(int, rec) int { return 410 } // 영구 오류: 바로 실패(포기)
	c := e.sub("실패 큐", nil)
	for i := int64(1); i <= 3; i++ {
		e.note(changes.ResItem, i, changes.OpUpdated, "")
	}
	e.tick()
	if e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'failed'`) != 3 {
		t.Fatalf("실패 건: %d", e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'failed'`))
	}
	ctx := e.ctx()
	n, err := e.svc.ResendFailed(ctx, c.View.ID)
	if err != nil || n != 3 {
		t.Fatalf("일괄 재전송 %d %v", n, err)
	}
	if e.count(`SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'queued' AND resends = 1`) != 3 {
		t.Fatal("대기로 돌아가야 한다")
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.delivery_resend' AND diff LIKE '%"new":"3"%'`) != 1 {
		t.Fatal("감사에 건수 3 이 한 건 남아야 한다")
	}
	// 실패한 건이 없으면 0건이고 감사도 없다.
	if n, err := e.svc.ResendFailed(ctx, c.View.ID); err != nil || n != 0 {
		t.Fatalf("대기 중인 구독: %d %v", n, err)
	}
	// 건마다 5번까지: 다시 실패시키며 반복하면 한도에서 멈춘다.
	total := 3
	for i := 0; i < 8; i++ {
		e.advance(3 * time.Hour)
		if _, err := e.svc.Resume(ctx, c.View.ID); err != nil { // 연속 실패 5건이면 자동 중지되므로 반복하려면 재개한다
			t.Fatal(err)
		}
		e.tick()
		n, err := e.svc.ResendFailed(ctx, c.View.ID)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total != 15 {
		t.Fatalf("건마다 5번까지(3건 × 5): 재전송 총 %d", total)
	}
	if _, err := e.svc.ResendFailed(ctx, 9999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 구독: %v", err)
	}
}

func TestAllDeliveriesFiltersAndPermission(t *testing.T) {
	e := newEnv(t)
	e.recv.status = func(n int, _ rec) int {
		if n == 1 {
			return 200
		}
		return 410
	}
	a := e.sub("가", nil)
	e.sub("나", func(ci *webhooks.CreateInput) { ci.Name = "나" })
	e.note(changes.ResItem, 1, changes.OpUpdated, "")
	e.note(changes.ResJournal, 2, changes.OpCreated, "draft")
	e.tick()
	ctx := e.ctx()
	all, err := e.svc.AllDeliveries(ctx, webhooks.DeliveryFilter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("전체 %d %v", len(all), err)
	}
	if got, _ := e.svc.AllDeliveries(ctx, webhooks.DeliveryFilter{SubID: a.View.ID}); len(got) != 2 {
		t.Fatalf("구독 필터 %d", len(got))
	}
	if got, _ := e.svc.AllDeliveries(ctx, webhooks.DeliveryFilter{Status: "failed"}); len(got) == 0 || got[0].Reason == "" || got[0].SubName == "" || got[0].Title == "" {
		t.Fatalf("상태 필터·한국어 사유: %+v", got)
	}
	if got, _ := e.svc.AllDeliveries(ctx, webhooks.DeliveryFilter{EventType: "journal.created"}); len(got) != 2 {
		t.Fatalf("종류 필터 %d", len(got))
	}
	for _, r := range all {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "http") || strings.Contains(string(b), "secret") {
			t.Fatalf("발송 기록에 주소·비밀: %s", b)
		}
	}
	// 감사(audit:read)는 볼 수 있고 직원은 못 본다.
	aud := contract.WithPrincipal(context.Background(), contract.Principal{UserID: e.admin.UserID, Role: contract.RoleAuditor, Channel: contract.ChannelWeb, Auth: contract.AuthSession})
	if got, err := e.svc.AllDeliveries(aud, webhooks.DeliveryFilter{}); err != nil || len(got) != 4 {
		t.Fatalf("감사: %d %v", len(got), err)
	}
	staff := contract.WithPrincipal(context.Background(), contract.Principal{UserID: e.admin.UserID, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession})
	if _, err := e.svc.AllDeliveries(staff, webhooks.DeliveryFilter{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	if _, err := e.svc.AllDeliveries(context.Background(), webhooks.DeliveryFilter{}); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("미인증: %v", err)
	}
}

func TestHostsSettingsValidatedAndAudited(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	if err := e.svc.SetHosts(ctx, webhooks.Hosts{AllowHosts: []string{"hooks.example.com", "*.corp.example"}, ExtraPorts: []string{"8443"}}); err != nil {
		t.Fatal(err)
	}
	h, err := e.svc.GetHosts(ctx)
	if err != nil || len(h.AllowHosts) != 2 || len(h.ExtraPorts) != 1 {
		t.Fatalf("설정 %+v %v", h, err)
	}
	for _, bad := range []webhooks.Hosts{{AllowHosts: []string{"a b"}}, {AllowHosts: []string{"http://x"}}, {ExtraPorts: []string{"99999"}}, {ExtraPorts: []string{"abc"}}} {
		if err := e.svc.SetHosts(ctx, bad); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'webhook.hosts_set' AND diff NOT LIKE '%hooks.example.com%'`) != 1 {
		t.Fatal("감사에는 건수만 있고 호스트 값은 없어야 한다")
	}
	// 비우면 제한이 풀린다.
	if err := e.svc.SetHosts(ctx, webhooks.Hosts{}); err != nil {
		t.Fatal(err)
	}
	if h, _ := e.svc.GetHosts(ctx); len(h.AllowHosts) != 0 {
		t.Fatal("비워지지 않음")
	}
	staff := contract.WithPrincipal(context.Background(), contract.Principal{UserID: e.admin.UserID, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession})
	if err := e.svc.SetHosts(staff, webhooks.Hosts{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없음: %v", err)
	}
}

func TestManagementRoutesSessionOnlyAndSecretsNeverListed(t *testing.T) {
	e := newEnv(t)
	rt := contract.NewRouteTable()
	e.svc.Register(rt)
	call := func(p contract.Principal, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req = req.WithContext(contract.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	body := `{"name":"화면 등록","url":"` + e.srv.URL + `/hook","events":["item.updated"],"includeApi":false}`
	code, out := call(e.admin, "POST", "/api/admin/integrations/webhooks", body)
	if code != http.StatusCreated {
		t.Fatalf("등록 %d %s", code, out)
	}
	var created struct {
		Subscription struct{ ID int64 } `json:"subscription"`
		Secret       string
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatalf("등록 응답에 비밀이 한 번 있어야 한다: %s", out)
	}
	code, list := call(e.admin, "GET", "/api/admin/integrations/webhooks", "")
	if code != 200 || strings.Contains(list, created.Secret) || strings.Contains(list, e.srv.URL) {
		t.Fatalf("목록에 비밀·주소가 있다: %d %s", code, list)
	}
	id := strconv.FormatInt(created.Subscription.ID, 10)
	// 개인 토큰으로는 변경 호출이 거부되고(로그인 세션 필요), 조회는 된다. 직원은 모두 거부.
	pat := e.admin
	pat.Auth = contract.AuthToken
	for _, c := range [][2]string{{"POST", "/pause"}, {"POST", "/resume"}, {"POST", "/rotate-secret"}, {"POST", "/test"}, {"POST", "/resend-failed"}} {
		if code, _ := call(pat, c[0], "/api/admin/integrations/webhooks/"+id+c[1], ""); code != http.StatusForbidden {
			t.Errorf("토큰으로 %s: %d", c[1], code)
		}
	}
	if code, _ := call(pat, "PUT", "/api/admin/integrations/webhooks/"+id, `{"name":"x"}`); code != http.StatusForbidden {
		t.Errorf("토큰으로 수정: %d", code)
	}
	staff := contract.Principal{UserID: e.admin.UserID, Role: contract.RoleStaff, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	for _, p := range []string{"/api/admin/integrations/webhooks", "/api/admin/integrations/webhook-events", "/api/admin/integrations/webhook-hosts"} {
		if code, _ := call(staff, "GET", p, ""); code != http.StatusForbidden {
			t.Errorf("직원 %s: %d", p, code)
		}
	}
	if code, _ := call(staff, "GET", "/api/admin/integrations/webhook-deliveries", ""); code != http.StatusForbidden {
		t.Errorf("직원 발송 기록: %d", code)
	}
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'access.denied' AND target_type = 'route' AND target_id LIKE '%webhook-deliveries%'`) != 1 {
		t.Fatal("발송 기록 조회 거부 감사가 한 건이어야 한다")
	}
	// 정상 흐름: 수정·중지·재개·시험·비밀 교체·일괄 재전송·발송 기록.
	if code, out := call(e.admin, "PUT", "/api/admin/integrations/webhooks/"+id, `{"name":"화면 수정","events":["item.updated","vendor.created"]}`); code != 200 || !strings.Contains(out, "화면 수정") {
		t.Fatalf("수정 %d %s", code, out)
	}
	for _, c := range []string{"pause", "resume"} {
		if code, _ := call(e.admin, "POST", "/api/admin/integrations/webhooks/"+id+"/"+c, ""); code != 200 {
			t.Fatalf("%s %d", c, code)
		}
	}
	if code, out := call(e.admin, "POST", "/api/admin/integrations/webhooks/"+id+"/test", ""); code != 200 || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("시험 보내기 %d %s", code, out)
	}
	if code, out := call(e.admin, "POST", "/api/admin/integrations/webhooks/"+id+"/rotate-secret", ""); code != 200 || !strings.Contains(out, "whsec_") {
		t.Fatalf("비밀 교체 %d %s", code, out)
	}
	if code, out := call(e.admin, "POST", "/api/admin/integrations/webhooks/"+id+"/resend-failed", ""); code != 200 || !strings.Contains(out, `"count":0`) {
		t.Fatalf("일괄 재전송 %d %s", code, out)
	}
	if code, out := call(e.admin, "GET", "/api/admin/integrations/webhook-events", ""); code != 200 || !strings.Contains(out, "journal.status_changed:approved") {
		t.Fatalf("이벤트 종류 %d", code)
	}
	if code, _ := call(e.admin, "GET", "/api/admin/integrations/webhook-deliveries?from=2026-13-01", ""); code != http.StatusBadRequest {
		t.Fatalf("날짜 형식: %d", code)
	}
	if code, _ := call(e.admin, "PUT", "/api/admin/integrations/webhook-hosts", `{"allowHosts":["a.example"],"extraPorts":[]}`); code != http.StatusNoContent {
		t.Fatalf("호스트 설정: %d", code)
	}
	if code, _ := call(e.admin, "POST", "/api/admin/integrations/webhooks/99999/pause", ""); code != http.StatusNotFound {
		t.Fatalf("없는 구독: %d", code)
	}
}

// 구독 주소의 경로는 어떤 형태로도 나가지 않는다: 목록 hint 는 호스트만이고, 이전 버전이 저장한 "호스트…끝6자" 도 표시 단계에서 호스트만 나간다. 감사에도 없다.
func TestSubscriptionHintIsHostOnlyEverywhere(t *testing.T) {
	e := newEnv(t)
	secretPath := "/hook/PATH-TOKEN-abc123"
	c := e.sub("힌트", func(ci *webhooks.CreateInput) { ci.URL = e.srv.URL + secretPath })
	u, _ := url.Parse(e.srv.URL)
	if c.View.Hint != u.Host {
		t.Fatalf("등록 응답의 hint: %q(기대 %q)", c.View.Hint, u.Host)
	}
	// 옛 형식으로 저장된 값을 되살려도 목록·응답에서는 호스트만 나간다.
	if _, err := e.db.Exec(`UPDATE webhook_subscriptions SET hint = ? WHERE id = ?`, u.Host+"…abc123", c.View.ID); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(e.ctx())
	if err != nil || len(list) != 1 || list[0].Hint != u.Host {
		t.Fatalf("옛 hint 표시: %+v %v", list, err)
	}
	b, _ := json.Marshal(list)
	for _, bad := range []string{"abc123", "PATH-TOKEN", "…", secretPath} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("목록에 경로 조각 %q: %s", bad, b)
		}
	}
	// 주소를 바꿔도(수정) 새 hint 는 호스트만이다.
	v, err := e.svc.Update(e.ctx(), c.View.ID, webhooks.UpdateInput{URL: str(e.srv.URL + "/moved/SECOND-TOKEN-xyz789")})
	if err != nil || v.Hint != u.Host {
		t.Fatalf("수정 응답 hint: %+v %v", v, err)
	}
	// 감사와 새로 저장된 hint 에도 경로 조각이 없다.
	if e.count(`SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%TOKEN%' OR detail LIKE '%TOKEN%' OR diff LIKE '%xyz789%' OR diff LIKE '%abc123%'`) != 0 {
		t.Fatal("감사에 경로 조각이 있다")
	}
	if e.count(`SELECT COUNT(*) FROM webhook_subscriptions WHERE hint LIKE '%/%' OR hint LIKE '%TOKEN%' OR hint LIKE '%xyz789%'`) != 0 {
		t.Fatal("새로 저장된 hint 에 경로 조각이 있다")
	}
}

// B44: 구독 목록의 법인 표시는 활성 법인이 둘 이상인 설치에서만 응답에 실린다.
func TestSubscriptionListShowsEntityOnlyWhenMultiEntity(t *testing.T) {
	e := newEnv(t)
	e.sub("법인 표시", nil)
	list, err := e.svc.List(e.ctx())
	if err != nil || len(list) != 1 || list[0].Entity != "" {
		t.Fatalf("법인이 하나면 법인 표시가 없다: %+v %v", list, err)
	}
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, business_no, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', '', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.List(e.ctx())
	if err != nil || list[0].Entity == "" {
		t.Fatalf("법인이 둘이면 법인 이름이 실린다: %+v %v", list, err)
	}
	if _, err := e.db.Exec(`UPDATE webhook_subscriptions SET entity_id = 0`); err != nil {
		t.Fatal(err)
	}
	if list, _ = e.svc.List(e.ctx()); list[0].Entity != "전체 법인" {
		t.Fatalf("전체 구독: %+v", list)
	}
}
