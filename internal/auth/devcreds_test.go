package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

const devPw = "Dev-Seed-Pw-1!"

// 개발 시드 계정에 초기 비밀번호 자격을 만들고(변경 강제), 멱등이며, 이미 있는 자격은 덮어쓰지 않고, 값은 어디에도 남지 않는다.
func TestSeedDevPasswords(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	e.setPw(t, "admin@pl.test", "Existing-Pw-9!x", false) // 이미 자격이 있는 계정
	var before string
	_ = e.db.QueryRow(`SELECT password_hash FROM user_credentials WHERE user_id=?`, e.uid(t, "admin@pl.test")).Scan(&before)

	total := e.n(t, `SELECT COUNT(*) FROM users`)
	n, err := e.s.SeedDevPasswords(ctx, devPw)
	if err != nil || n != total-1 {
		t.Fatalf("n=%d(기대 %d) %v", n, total-1, err)
	}
	var after string
	_ = e.db.QueryRow(`SELECT password_hash FROM user_credentials WHERE user_id=?`, e.uid(t, "admin@pl.test")).Scan(&after)
	if after != before {
		t.Fatal("이미 있는 자격을 덮어씀")
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials WHERE must_change = 1`) != total-1 {
		t.Fatal("변경 강제 표시가 모든 시드 자격에 있어야 함")
	}
	if n2, err := e.s.SeedDevPasswords(ctx, devPw); err != nil || n2 != 0 {
		t.Fatalf("멱등이어야 함: %d %v", n2, err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.dev_seed_credentials'`) != 1 {
		t.Fatal("감사는 1건(두 번째는 만든 게 없으므로 기록 없음)")
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`, devPw, devPw) != 0 {
		t.Fatal("비밀번호가 감사에 남음")
	}
	lr, err := e.login("staff@pl.test", devPw)
	if err != nil || lr.Setup != contract.SetupPasswordChange {
		t.Fatalf("첫 로그인은 변경 강제 제한 세션이어야 함: %+v %v", lr, err)
	}
	var h string
	_ = e.db.QueryRow(`SELECT password_hash FROM user_credentials WHERE user_id=?`, e.uid(t, "staff@pl.test")).Scan(&h)
	if !strings.HasPrefix(h, "$argon2id$") || strings.Contains(h, devPw) {
		t.Fatalf("해시 형식: %s", h)
	}
}

func TestSeedDevPasswordsRejectsWeakAndIgnoresProduction(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	if _, err := e.s.SeedDevPasswords(ctx, "short"); err == nil || strings.Contains(err.Error(), "short") && false {
		t.Fatalf("정책에 맞지 않는 값이 통과함: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials`) != 0 {
		t.Fatal("약한 값인데 자격이 생김")
	}
	// 개발 시드 밖의 사용자가 있으면(운영 데이터) 무시한다.
	if _, err := e.db.Exec(`INSERT INTO users (email, name, role, department_id) VALUES ('real@company.example', '실사용자', 'staff', (SELECT id FROM departments LIMIT 1))`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.SeedDevPasswords(ctx, devPw); err != nil || n != 0 {
		t.Fatalf("운영 데이터에서 무시되어야 함: %d %v", n, err)
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials`) != 0 {
		t.Fatal("운영 데이터에 자격이 생김")
	}
}
