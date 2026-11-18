package onboarding

import (
	"strings"
	"testing"
)

// B49 항목 "AI 비용 배부": 해당 없음 가능. 규칙·환율·확정한 달이 모두 있으면 완료, 일부만 있으면 진행 중, 없으면 미착수.
func TestAICostItem(t *testing.T) {
	x := newEnv(t, Env{})
	it := x.item(t, "ai_cost")
	if it.Title != "AI 비용 배부" || !it.Skippable || it.State != StateTodo || it.Evidence == "" {
		t.Fatalf("새 DB: %+v", it)
	}
	x.exec(t, `INSERT INTO ai_cost_rule_sets (version, valid_from_month, created_by, created_at) VALUES (1, '2026-01', ?, 't')`, x.admin.UserID)
	if it := x.item(t, "ai_cost"); it.State != StateInProgress || !strings.Contains(it.Evidence, "배부 규칙 1개 버전") {
		t.Fatalf("규칙만 있음: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `INSERT INTO ai_cost_fx (month, usd_krw_x1000, set_by, set_at) VALUES ('2026-10', 1350000, ?, 't')`, x.admin.UserID)
	if it := x.item(t, "ai_cost"); it.State != StateInProgress {
		t.Fatalf("규칙·환율만 있음: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `INSERT INTO ai_cost_runs (month, entity_id, rule_version, status, fx_usd_krw_x1000, total_krw, direct_krw, shared_krw, confirmed_by, confirmed_at) VALUES ('2026-10', 1, 1, 'confirmed', 1350000, 0, 0, 0, ?, 't')`, x.admin.UserID)
	if it := x.item(t, "ai_cost"); it.State != StateDone || !strings.Contains(it.Evidence, "확정한 달 1개") {
		t.Fatalf("확정한 달 있음: %s(%s)", it.State, it.Evidence)
	}
	// 해제된 달만 있으면 완료가 아니다.
	x.exec(t, `UPDATE ai_cost_runs SET status = 'released', released_by = ?, released_at = 't'`, x.admin.UserID)
	if it := x.item(t, "ai_cost"); it.State != StateInProgress {
		t.Fatalf("해제됨: %s(%s)", it.State, it.Evidence)
	}
}
