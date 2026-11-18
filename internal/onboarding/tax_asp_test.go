package onboarding

import (
	"strings"
	"testing"
)

// B43 항목 "전자세금계산서 연동": 해당 없음 가능. 켜져 있고 인증서가 만료 30일 밖이며 운영에서 모의가 아니면 완료.
func TestTaxASPItem(t *testing.T) {
	x := newEnv(t, Env{})
	it := x.item(t, "tax_asp")
	if it.Title != "전자세금계산서 연동" || !it.Skippable || it.State != StateTodo {
		t.Fatalf("새 DB: %+v", it)
	}
	// 시험 시계는 2026-11-13.
	x.exec(t, `INSERT INTO tax_asp_settings (id, provider, secret_sealed, cert_not_after, active, updated_at) VALUES (1, 'real', 'x', '2027-06-01', 0, '2026-11-13T00:00:00Z')`)
	if it := x.item(t, "tax_asp"); it.State != StateInProgress {
		t.Fatalf("꺼짐: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `UPDATE tax_asp_settings SET active = 1`)
	if it := x.item(t, "tax_asp"); it.State != StateDone {
		t.Fatalf("켜짐·유효: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `UPDATE tax_asp_settings SET cert_not_after = '2026-12-01'`)
	if it := x.item(t, "tax_asp"); it.State != StateInProgress || !strings.Contains(it.Evidence, "만료 18일 전") {
		t.Fatalf("만료 임박: %s(%s)", it.State, it.Evidence)
	}
	x.exec(t, `UPDATE tax_asp_settings SET cert_not_after = '2026-11-01'`)
	if it := x.item(t, "tax_asp"); it.State != StateInProgress || !strings.Contains(it.Evidence, "만료되어") {
		t.Fatalf("만료: %s(%s)", it.State, it.Evidence)
	}
	// 운영(DevIDP 꺼짐)에서 모의는 미완료, 개발 모드에서는 완료.
	x.exec(t, `UPDATE tax_asp_settings SET provider = 'mock', cert_not_after = ''`)
	if it := x.item(t, "tax_asp"); it.State != StateInProgress || !strings.Contains(it.Evidence, "모의") {
		t.Fatalf("운영 모의: %s(%s)", it.State, it.Evidence)
	}
	d := newEnv(t, Env{DevIDP: true})
	d.exec(t, `INSERT INTO tax_asp_settings (id, provider, secret_sealed, active, updated_at) VALUES (1, 'mock', '', 1, '2026-11-13T00:00:00Z')`)
	if it := d.item(t, "tax_asp"); it.State != StateDone {
		t.Fatalf("개발 모의: %s(%s)", it.State, it.Evidence)
	}
	// 수신 동기화가 연동 담당자 비활성으로 멈췄으면 진행 중으로 알린다.
	d.exec(t, `UPDATE tax_sync_state SET last_result = 'owner_inactive' WHERE id = 1`)
	if it := d.item(t, "tax_asp"); it.State != StateInProgress || !strings.Contains(it.Evidence, "동기화가 멈췄") {
		t.Fatalf("동기화 정지: %s(%s)", it.State, it.Evidence)
	}
}
