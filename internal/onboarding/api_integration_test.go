package onboarding

import (
	"strings"
	"testing"
)

// B42 항목 "연동 API": 해당 없음 가능. 켜져 있고 유효 토큰이 있으며 만료 14일 안인 토큰이 없으면 완료.
func TestAPIIntegrationItem(t *testing.T) {
	x := newEnv(t, Env{})
	it := x.item(t, "api_integration")
	if it.Title != "연동 API" || !it.Skippable || it.State != StateTodo {
		t.Fatalf("새 DB: %+v", it)
	}
	x.exec(t, `INSERT INTO app_settings (key, value, updated_at) VALUES ('api.enabled', '1', '2026-11-13T00:00:00Z')`)
	if it := x.item(t, "api_integration"); it.State != StateInProgress {
		t.Fatalf("켰지만 토큰 없음: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('t', 'pli_AAAA', 'h1', '["items:read"]', ?, '2026-11-01T00:00:00Z', '2027-05-01T00:00:00Z')`, x.admin.UserID)
	if it := x.item(t, "api_integration"); it.State != StateDone {
		t.Fatalf("켜짐·토큰 있음: %s(%s)", it.State, it.Evidence)
	}
	// 시험 시계는 2026-11-13: 11-20 만료는 14일 안이라 미완료로 돌아간다.
	x.exec(t, `INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('soon', 'pli_BBBB', 'h2', '["items:read"]', ?, '2026-11-01T00:00:00Z', '2026-11-20T00:00:00Z')`, x.admin.UserID)
	if it := x.item(t, "api_integration"); it.State != StateInProgress {
		t.Fatalf("만료 임박: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `UPDATE api_tokens SET revoked_at = '2026-11-13T00:00:00Z' WHERE token_hash = 'h2'`)
	if it := x.item(t, "api_integration"); it.State != StateDone {
		t.Fatalf("임박 토큰 폐기 뒤: %s", it.State)
	}
	// 쓰기 범위 토큰이 있으면 근거에 안내가 붙는다(상태는 바꾸지 않는다).
	x.exec(t, `INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('writer', 'pli_CCCC', 'h3', '["journals:draft","items:read"]', ?, '2026-11-01T00:00:00Z', '2027-05-01T00:00:00Z')`, x.admin.UserID)
	if it := x.item(t, "api_integration"); it.State != StateDone || !strings.Contains(it.Evidence, "쓰기 범위 토큰 1개") {
		t.Fatalf("쓰기 토큰 안내: %s(%s)", it.State, it.Evidence)
	}
	// 변경 웹훅(B42 2단계): 구독이 있는데 접속 주소(PL_BASE_URL)가 없으면 진행 중, 주소가 있으면 완료로 돌아간다. 중지된 구독이 있으면 진행 중.
	x.exec(t, `INSERT INTO webhook_subscriptions (name, secret_sealed, events_json, created_at, updated_at) VALUES ('hook', 'x', '["item.updated"]', '2026-11-13T00:00:00Z', '2026-11-13T00:00:00Z')`)
	if it := x.item(t, "api_integration"); it.State != StateInProgress || !strings.Contains(it.Evidence, "PL_BASE_URL") {
		t.Fatalf("접속 주소 없음: %s(%s)", it.State, it.Evidence)
	}
	y := newEnv(t, Env{BaseURLSet: true})
	y.exec(t, `INSERT INTO app_settings (key, value, updated_at) VALUES ('api.enabled', '1', '2026-11-13T00:00:00Z')`)
	y.exec(t, `INSERT INTO api_tokens (name, token_prefix, token_hash, scopes, created_by, created_at, expires_at) VALUES ('t', 'pli_AAAA', 'h1', '["items:read"]', ?, '2026-11-01T00:00:00Z', '2027-05-01T00:00:00Z')`, y.admin.UserID)
	y.exec(t, `INSERT INTO webhook_subscriptions (name, secret_sealed, events_json, created_at, updated_at) VALUES ('hook', 'x', '["item.updated"]', '2026-11-13T00:00:00Z', '2026-11-13T00:00:00Z')`)
	if it := y.item(t, "api_integration"); it.State != StateDone || !strings.Contains(it.Evidence, "변경 웹훅 구독 1개") {
		t.Fatalf("주소 있음: %s(%s)", it.State, it.Evidence)
	}
	y.exec(t, `UPDATE webhook_subscriptions SET paused_reason = 'auto'`)
	if it := y.item(t, "api_integration"); it.State != StateInProgress || !strings.Contains(it.Evidence, "중지된 구독 1개") {
		t.Fatalf("자동 중지: %s(%s)", it.State, it.Evidence)
	}
}
