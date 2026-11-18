package service_test

import (
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"strings"
	"testing"
)

// 도입 이전(0011 백필) 진행 중 건은 단계의 직책·배정자 이름 스냅샷이 비어 있고 후보가 여럿일 수 있다. 직책은 단계 종류에서 채우고 후보는 모두 보인다.
func TestLegacyStepDisplaysTitleAndAllCandidates(t *testing.T) {
	dbtest.SkipPG(t, "SQLite 트리거·PRAGMA 를 직접 쓴다")
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := e.db.Exec(`DROP TRIGGER trg_approval_steps_snapshot_fixed`); err != nil { // 도입 이전 행을 흉내 내려고 스냅샷 보호를 푼다
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE approval_steps SET title = '', unit_path = '' WHERE pr_id = ?`, pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (?,?,1,10,'구매팀장2')`, pr.ID, pr.Round); err != nil {
		t.Fatal(err)
	}
	got, err := e.purchase.Get(as(uStaff), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := got.Steps[0]
	if st.Title != "팀장" {
		t.Fatalf("직책 이름이 비었다: %+v", st)
	}
	if !strings.Contains(st.AssigneeName, "팀장") || !strings.Contains(st.AssigneeName, "구매팀장2") {
		t.Fatalf("후보가 모두 보여야 함: %q", st.AssigneeName)
	}
}
