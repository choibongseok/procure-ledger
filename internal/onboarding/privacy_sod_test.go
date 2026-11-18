package onboarding

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// B41 항목 "개인정보 기록·권한 분리": 해당 없음 불가, 보관 기간·하루 요약·차단 규칙·감사 담당 네 조건으로 완료가 정해진다.
func TestPrivacySoDItem(t *testing.T) {
	x := newEnv(t, Env{})
	it := x.item(t, "privacy_sod")
	if it.Title != "개인정보 기록·권한 분리" || it.Skippable {
		t.Fatalf("항목: %+v", it)
	}
	// 새 DB: 차단 규칙(초기 모드)은 있으나 보관 기간 미설정·감사 담당 없음 → 진행 중.
	if it.State != StateInProgress {
		t.Fatalf("새 DB 상태 %s(%s)", it.State, it.Evidence)
	}
	// 보관 기간을 정하고 감사 담당이 생기면 완료(조회 기록이 아직 없으니 하루 요약은 필요 없다).
	x.exec(t, `INSERT INTO app_settings (key, value, updated_at) VALUES ('privacy.retention_days', '730', '2026-11-13T00:00:00Z')`)
	var aid int64
	if err := x.db.QueryRow(`INSERT INTO users (email, name, role, department_id) VALUES ('aud@x.test', '감사', 'auditor', (SELECT id FROM departments WHERE name = '운영')) RETURNING id`).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	txs := dbutil.NewTxManager(x.db, dbutil.DialectOf(x.db))
	if _, err := access.New(x.db, txs, audit.New(x.db), func() time.Time { return *x.clock }).Reconcile(x.ctx, contract.Principal{Role: "system", Channel: contract.ChannelWeb}, true); err != nil {
		t.Fatal(err)
	}
	if it := x.item(t, "privacy_sod"); it.State != StateDone {
		t.Fatalf("조건을 채운 뒤 상태 %s(%s)", it.State, it.Evidence)
	}
	// 조회 기록이 있는데 최근 하루 요약이 없으면 다시 진행 중이 된다.
	x.exec(t, `INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('2026-11-12T01:00:00.000000000Z', 1, 'admin', 'web', 'contact', '조직도 사람 카드', 1, 'h', '', 'x1')`)
	if it := x.item(t, "privacy_sod"); it.State != StateInProgress {
		t.Fatalf("요약 없음 상태 %s(%s)", it.State, it.Evidence)
	}
	// 실제 감사 기록기로 하루 요약을 남긴다(해시 체인 열을 시험이 직접 채우지 않는다).
	rec := audit.New(x.db)
	if err := txs.WithTx(x.ctx, func(tx contract.Tx) error {
		return rec.Record(x.ctx, tx, caudit.Entry{Actor: contract.Principal{Role: "system", Channel: contract.ChannelWeb}, Action: "privacy.log_digest", TargetType: caudit.TargetPrivacyLog, TargetID: "2026-11-12", Result: caudit.ResultSuccess})
	}); err != nil {
		t.Fatal(err)
	}
	if it := x.item(t, "privacy_sod"); it.State != StateDone {
		t.Fatalf("요약 뒤 상태 %s(%s)", it.State, it.Evidence)
	}
}
