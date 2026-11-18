package auth

import (
	"context"
	"database/sql"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/access"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

var lowArgon = ArgonParams{MemoryKiB: 64, Iterations: 1, Parallelism: 1}

type fakeMailer struct {
	mu   sync.Mutex
	sent []fakeMail
	fail bool
}
type fakeMail struct{ to, subject, body string }

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("smtp down")
	}
	m.sent = append(m.sent, fakeMail{to, subject, body})
	return nil
}
func (m *fakeMailer) last() fakeMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return fakeMail{}
	}
	return m.sent[len(m.sent)-1]
}

type pwEnv struct {
	s     *Service
	db    *sql.DB
	mail  *fakeMailer
	clock *time.Time
	admin contract.Principal
}

func newPwEnv(t *testing.T, withMail bool) *pwEnv {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil { // 기본 역할 템플릿 권한(B33)
		t.Fatal(err)
	}
	if err := SeedDevUsers(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	e := &pwEnv{db: db, clock: &now, mail: &fakeMailer{}}
	opt := Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: audit.New(db), Argon: lowArgon, SyncMail: true,
		BaseURL: "http://localhost:8080", Now: func() time.Time { return *e.clock }}
	if withMail {
		opt.Mailer = e.mail
	}
	var err error
	e.s, err = New(opt)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	_ = db.QueryRow(`SELECT id FROM users WHERE email='admin@pl.test'`).Scan(&id)
	e.admin = contract.Principal{UserID: id, Name: "관리자", Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	return e
}

func (e *pwEnv) uid(t *testing.T, email string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// setPw 는 정책을 거치지 않고 비밀번호를 직접 심는다(시험 준비용).
func (e *pwEnv) setPw(t *testing.T, email, pw string, must bool) {
	t.Helper()
	h, err := e.s.hasher.Hash(context.Background(), pw)
	if err != nil {
		t.Fatal(err)
	}
	id := e.uid(t, email)
	err = e.s.txs.WithTx(context.Background(), func(tx contract.Tx) error {
		return e.s.setPassword(context.Background(), tx, id, h, "", DefaultPasswordPolicy(), must)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *pwEnv) login(email, pw string) (LoginResult, error) {
	return e.s.LoginPassword(context.Background(), PasswordLogin{Email: email, Password: pw})
}

func (e *pwEnv) n(t *testing.T, q string, args ...any) int { return count(t, e.db, q, args...) }

const goodPw = "Tr0ub4dor&Horse"

// AC-1000: 계정만 있고 비밀번호가 없으면 로그인할 수 없다.
func TestNoPasswordNoLogin(t *testing.T) {
	e := newPwEnv(t, false)
	if _, err := e.login("staff@pl.test", goodPw); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("비밀번호 없는 계정이 로그인됨: %v", err)
	}
}

// AC-1001: argon2id PHC 형식, 파라미터를 올려도 기존 해시가 검증되고 로그인 때 재해시된다.
func TestHashPHCAndRehash(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	var h string
	_ = e.db.QueryRow(`SELECT password_hash FROM user_credentials WHERE user_id=?`, e.uid(t, "staff@pl.test")).Scan(&h)
	if !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("PHC 형식 아님: %s", h)
	}
	if strings.Contains(h, goodPw) {
		t.Fatal("원문이 해시에 들어 있음")
	}
	e.s.hasher = newHasher(ArgonParams{MemoryKiB: 128, Iterations: 2, Parallelism: 1}) // 비용을 올린다
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatalf("낮은 비용의 기존 해시가 검증되지 않음: %v", err)
	}
	var h2 string
	_ = e.db.QueryRow(`SELECT password_hash FROM user_credentials WHERE user_id=?`, e.uid(t, "staff@pl.test")).Scan(&h2)
	if !strings.Contains(h2, "m=128,t=2,p=1") {
		t.Fatalf("재해시되지 않음: %s", h2)
	}
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatalf("재해시 뒤 로그인 실패: %v", err)
	}
}

// 기본 비용이 명세(64MiB·3·2)와 같다.
func TestDefaultArgonCost(t *testing.T) {
	if DefaultArgon.MemoryKiB != 65536 || DefaultArgon.Iterations != 3 || DefaultArgon.Parallelism != 2 {
		t.Fatalf("기본 비용: %+v", DefaultArgon)
	}
}

// AC-1002: 정책 경계값.
func TestPasswordPolicyBoundaries(t *testing.T) {
	pp := DefaultPasswordPolicy()
	cases := []struct {
		pw   string
		want string // 빈 문자열이면 통과
	}{
		{"Abcdefgh1", "10자 이상"},                // 9자
		{"Abcdefghi1", ""},                     // 10자, 3종
		{"abcdefghij", "3종류"},                  // 1종
		{"abcdefgh12", "3종류"},                  // 2종
		{"abcdefg12!", ""},                     // 3종(소문자·숫자·기호)
		{strings.Repeat("aB1", 43), "128자"},    // 129자
		{strings.Repeat("aB1", 42) + "aB", ""}, // 128자
		{"Password123", "흔한"},
		{"한글비밀번호입니다1aB", ""}, // 유니코드
	}
	for _, c := range cases {
		err := pp.Check(c.pw, "kim@pl.test", "김철수")
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q 통과해야 함: %v", c.pw, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q 거부 문구에 %q 기대: %v", c.pw, c.want, err)
		}
	}
	if err := pp.Check("김철수김철수김철수김철수", "a@b.c", "김철수김철수김철수김철수"); err == nil {
		t.Error("이름과 같은 비밀번호가 통과함")
	}
	if err := pp.Check("kim.cheolsu-abc", "kim.cheolsu-abc@x.test", ""); err == nil {
		t.Error("이메일 앞부분과 같은 비밀번호가 통과함")
	}
	var pe PolicyError
	if err := pp.Check("short", "", ""); !errors.As(err, &pe) || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "지금 5자") {
		t.Errorf("구체 문구·ErrValidation 이어야 함: %v", err)
	}
}

func TestTempPasswordSatisfiesPolicy(t *testing.T) {
	pp := DefaultPasswordPolicy()
	for i := 0; i < 50; i++ {
		pw, err := generateTempPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len([]rune(pw)) != 14 {
			t.Fatalf("길이 %d", len(pw))
		}
		if err := pp.Check(pw, "", ""); err != nil {
			t.Fatalf("%q: %v", pw, err)
		}
	}
}

// AC-1010·1011: 5회 실패 15분 잠금 경계, 잠금 중 정답 거부, 만료 시각 비연장, 감사 1행.
func TestLockoutBoundaries(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	for i := 1; i <= 4; i++ {
		if _, err := e.login("staff@pl.test", "wrong-Pass1!"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("%d회째: %v", i, err)
		}
	}
	// 4회 실패 뒤 정답은 로그인되고 횟수가 0 이 된다.
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatalf("4회 실패 뒤 정답이 거부됨: %v", err)
	}
	for i := 1; i <= 4; i++ {
		_, _ = e.login("staff@pl.test", "wrong-Pass1!")
	}
	_, err := e.login("staff@pl.test", "wrong-Pass1!") // 5회째
	var lk LockedError
	if !errors.As(err, &lk) {
		t.Fatalf("5회째에 잠겨야 함: %v", err)
	}
	until := lk.Until
	if !until.Equal(e.clock.Add(15 * time.Minute)) {
		t.Fatalf("잠금 시각 %v", until)
	}
	*e.clock = e.clock.Add(5 * time.Minute)
	if _, err := e.login("staff@pl.test", goodPw); !errors.As(err, &lk) { // 잠금 중 정답
		t.Fatalf("잠금 중 정답이 통과함: %v", err)
	}
	_, _ = e.login("staff@pl.test", "wrong-Pass1!") // 잠금 중 실패는 연장하지 않는다
	var cur string
	_ = e.db.QueryRow(`SELECT locked_until FROM user_credentials WHERE user_id=?`, e.uid(t, "staff@pl.test")).Scan(&cur)
	if !parseTime(cur).Equal(until) {
		t.Fatalf("잠금 만료가 연장됨: %s", cur)
	}
	if n := e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.account_lock'`); n != 1 {
		t.Fatalf("잠금 감사 %d행(1행이어야 함)", n)
	}
	if n := e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'auth.%' AND result='denied'`); n != 0 {
		t.Fatalf("실패마다 감사 행이 생김: %d", n)
	}
	*e.clock = until.Add(time.Second) // 15분 직후
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatalf("잠금 만료 뒤 로그인 실패: %v", err)
	}
}

// AC-1011: 관리자 해제는 즉시 적용되고 감사가 남는다. 권한 없는 사람은 못 한다.
func TestAdminUnlock(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	for i := 0; i < 5; i++ {
		_, _ = e.login("staff@pl.test", "wrong-Pass1!")
	}
	if _, err := e.login("staff@pl.test", goodPw); err == nil {
		t.Fatal("잠겨 있어야 함")
	}
	staff := contract.Principal{UserID: e.uid(t, "staff@pl.test"), Role: contract.RoleStaff}
	if err := e.s.UnlockAccount(context.Background(), staff, staff.UserID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("일반 사용자가 해제함: %v", err)
	}
	if err := e.s.UnlockAccount(context.Background(), e.admin, e.uid(t, "staff@pl.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatalf("해제 뒤 로그인 실패: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.account_unlock'`) != 1 {
		t.Fatal("해제 감사 없음")
	}
}

// AC-1012: 없는 이메일·비밀번호 없는 계정·퇴사 계정·틀린 비밀번호가 같은 오류이고 잠금 표를 만들지 않는다.
func TestNoAccountEnumeration(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	e.setPw(t, "finance@pl.test", goodPw, false)
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE email = 'finance@pl.test'`); err != nil {
		t.Fatal(err)
	}
	cases := [][2]string{
		{"nobody@pl.test", goodPw},          // 없는 이메일
		{"manager@pl.test", goodPw},         // 비밀번호 없는 계정
		{"finance@pl.test", goodPw},         // 비활성 계정(정답 비밀번호)
		{"staff@pl.test", "wrong-Pass1!"},   // 틀린 비밀번호
		{"  STAFF@pl.test ", "wrong-Pass2"}, // 정규화된 이메일 + 틀림
	}
	var first string
	for _, c := range cases {
		_, err := e.login(c[0], c[1])
		if !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("%v: %v", c, err)
		}
		if first == "" {
			first = err.Error()
		}
		if err.Error() != first {
			t.Fatalf("오류 문구가 다름: %q vs %q", err.Error(), first)
		}
	}
	if n := e.n(t, `SELECT COUNT(*) FROM user_credentials`); n != 2 {
		t.Fatalf("잠금 표가 늘어남: %d", n)
	}
}

// 계정 열거 방지의 시간 차 방어를 결정적으로 본다(벽시계 비교는 부하에 흔들린다): 비밀번호를 검증하는 모든 경로(없는 이메일·
// 자격 없는 계정·비활성·틀린 비밀번호·정답)는 같은 비용의 argon2 검증을 정확히 한 번 호출한다.
func TestEveryLoginPathVerifiesExactlyOnce(t *testing.T) {
	e := newPwEnv(t, false)
	<-e.s.dummyDone // 기동 때 만든 더미 해시가 끝난 뒤에 센다
	e.setPw(t, "staff@pl.test", goodPw, false)
	e.setPw(t, "finance@pl.test", goodPw, false)
	e.db.Exec(`UPDATE users SET active = 0 WHERE email = 'finance@pl.test'`)
	cases := []struct{ name, email, pw string }{
		{"없는 이메일", "nobody@pl.test", "wrong-Pass1!"},
		{"자격 없는 계정", "manager@pl.test", "wrong-Pass1!"},
		{"비활성 계정(틀린 비밀번호)", "finance@pl.test", "wrong-Pass1!"},
		{"비활성 계정(정답)", "finance@pl.test", goodPw},
		{"틀린 비밀번호", "staff@pl.test", "wrong-Pass1!"},
		{"정답", "staff@pl.test", goodPw},
		{"대문자·공백 이메일", "  STAFF@pl.test ", "wrong-Pass1!"},
		{"빈 이메일", "", "wrong-Pass1!"},
	}
	for _, c := range cases {
		before := e.s.hasher.verifies.Load()
		_, _ = e.login(c.email, c.pw)
		if got := e.s.hasher.verifies.Load() - before; got != 1 {
			t.Errorf("%s: 검증 호출 %d번(정확히 1번이어야 함)", c.name, got)
		}
		e.db.Exec(`UPDATE user_credentials SET failed_count = 0, locked_until = NULL`)
	}
	// 더미 해시는 실제 자격과 같은 설정 비용으로 만든다.
	p, _, _, err := parsePHC(e.s.dummyHash)
	if err != nil || p != e.s.hasher.params {
		t.Fatalf("더미 해시 비용 %+v != 설정 비용 %+v (%v)", p, e.s.hasher.params, err)
	}
}

// B20: 퇴사(재직 아님) 계정은 정답 비밀번호로도 같은 오류이고 감사에 사유가 남는다.
func TestPasswordLoginDeniedWhenNotEmployed(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "lead@pl.test", goodPw, false)
	uid := e.uid(t, "lead@pl.test")
	dbtest.FKOff(t, e.db)
	_, _ = e.db.Exec(`INSERT INTO org_write_guard (id) VALUES (1) ON CONFLICT DO NOTHING`) // 구간 표 보호 트리거 우회(시험 전용)
	if _, err := e.db.Exec(`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id) VALUES (?, '2020-01-01', '2026-10-01', 0, 1, 1), (?, '2026-10-01', NULL, 1, 1, 1)`, uid, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login("lead@pl.test", goodPw); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("퇴사자가 로그인함: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='denied' AND diff LIKE '%not_employed%'`) != 1 {
		t.Fatal("거부 감사 없음")
	}
	if _, err := e.s.StartLogin(context.Background(), e.admin, uid, "temp"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("퇴사자에게 로그인 시작이 됨: %v", err)
	}
}

// AC-1003: 임시 비밀번호는 제한 세션을 만들고, 변경하면 풀린다. 임시 비밀번호는 한 번만 보이고 DB 평문이 없다.
func TestTempPasswordFlow(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	staff := e.uid(t, "staff@pl.test")
	if _, err := e.s.StartLogin(ctx, e.admin, e.admin.UserID, "temp"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("관리자 본인에게 임시 비밀번호가 발급됨: %v", err)
	}
	if _, err := e.s.StartLogin(ctx, e.admin, staff, "mail"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("SMTP 없이 mail 이 됨: %v", err)
	}
	if _, err := e.s.StartLogin(ctx, contract.Principal{UserID: staff, Role: contract.RoleStaff}, staff, "temp"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없이 발급됨: %v", err)
	}
	res, err := e.s.StartLogin(ctx, e.admin, staff, "temp")
	if err != nil || res.TempPassword == "" || res.ExpiresAt == nil {
		t.Fatalf("%+v %v", res, err)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM user_credentials WHERE password_hash LIKE '%' || ? || '%'`, `SELECT COUNT(*) FROM audit_logs WHERE diff LIKE '%' || ? || '%' OR detail LIKE '%' || ? || '%'`} {
		args := []any{res.TempPassword}
		if strings.Count(q, "?") == 2 {
			args = append(args, res.TempPassword)
		}
		if e.n(t, q, args...) != 0 {
			t.Fatalf("임시 비밀번호가 저장소에 남음: %s", q)
		}
	}
	lr, err := e.login("staff@pl.test", res.TempPassword)
	if err != nil || lr.Setup != contract.SetupPasswordChange {
		t.Fatalf("제한 세션이어야 함: %+v %v", lr, err)
	}
	p := contract.Principal{UserID: staff, Role: contract.RoleStaff, Setup: lr.Setup, Auth: contract.AuthSession}
	if p.Can(contract.PermPurchaseRead) {
		t.Fatal("제한 세션이 업무 권한을 가짐")
	}
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, res.TempPassword, "short"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("정책 위반이 통과함: %v", err)
	}
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, "bad-current-1A", goodPw); err == nil {
		t.Fatal("현재 비밀번호가 틀렸는데 통과함")
	}
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, res.TempPassword, goodPw); err != nil {
		t.Fatal(err)
	}
	var setup sql.NullString
	_ = e.db.QueryRow(`SELECT setup FROM sessions WHERE id_hash=?`, sha256Hex(lr.RawSession)).Scan(&setup)
	if setup.Valid {
		t.Fatalf("변경 뒤에도 제한 세션: %s", setup.String)
	}
	if _, err := e.login("staff@pl.test", res.TempPassword); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("바꾼 뒤에도 임시 비밀번호가 통한다")
	}
	lr2, err := e.login("staff@pl.test", goodPw)
	if err != nil || lr2.Setup != "" {
		t.Fatalf("%+v %v", lr2, err)
	}
	// 재사용 금지: 방금 쓴 비밀번호로 다시 바꿀 수 없다.
	p2 := contract.Principal{UserID: staff, Role: contract.RoleStaff, Auth: contract.AuthSession}
	if err := e.s.ChangePassword(ctx, p2, lr2.RawSession, goodPw, goodPw); err == nil || !strings.Contains(err.Error(), "다시 쓸 수 없") {
		t.Fatalf("재사용 금지 실패: %v", err)
	}
}

// 임시 비밀번호는 24시간이 지나면 쓸 수 없다.
func TestTempPasswordExpires(t *testing.T) {
	e := newPwEnv(t, false)
	res, err := e.s.StartLogin(context.Background(), e.admin, e.uid(t, "staff@pl.test"), "temp")
	if err != nil {
		t.Fatal(err)
	}
	*e.clock = e.clock.Add(25 * time.Hour)
	if _, err := e.login("staff@pl.test", res.TempPassword); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("만료된 임시 비밀번호가 통함: %v", err)
	}
}

// 재사용 금지는 최근 3개다.
func TestPasswordHistoryThree(t *testing.T) {
	e := newPwEnv(t, false)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", "Pw-Alpha-001!", false)
	uid := e.uid(t, "staff@pl.test")
	p := contract.Principal{UserID: uid, Role: contract.RoleStaff, Auth: contract.AuthSession}
	lr, _ := e.login("staff@pl.test", "Pw-Alpha-001!")
	cur := "Pw-Alpha-001!"
	for _, next := range []string{"Pw-Bravo-002!", "Pw-Charl-003!"} {
		if err := e.s.ChangePassword(ctx, p, lr.RawSession, cur, next); err != nil {
			t.Fatal(err)
		}
		cur = next
	}
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, cur, "Pw-Alpha-001!"); err == nil {
		t.Fatal("3개 전 비밀번호가 재사용됨")
	}
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, cur, "Pw-Delta-004!"); err != nil {
		t.Fatal(err)
	}
	cur = "Pw-Delta-004!"
	if err := e.s.ChangePassword(ctx, p, lr.RawSession, cur, "Pw-Alpha-001!"); err != nil {
		t.Fatalf("3개보다 오래된 비밀번호는 쓸 수 있어야 함: %v", err)
	}
}

// AC-1020·1021: 재설정 링크 30분·1회용·해시만 저장·새 발급 시 이전 무효, 응답 동일, 성공 시 세션 폐기·잠금 해제.
func TestResetLinkFlow(t *testing.T) {
	e := newPwEnv(t, true)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, _ := e.login("staff@pl.test", goodPw)
	for i := 0; i < 5; i++ {
		_, _ = e.login("staff@pl.test", "wrong-Pass1!")
	}
	// 없는 이메일·비밀번호 없는 계정은 메일이 가지 않고 같은 결과다.
	for _, em := range []string{"nobody@pl.test", "manager@pl.test"} {
		if err := e.s.RequestReset(ctx, em); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.mail.sent) != 0 {
		t.Fatal("계정이 없는데 메일이 나감")
	}
	if err := e.s.RequestReset(ctx, "STAFF@pl.test "); err != nil {
		t.Fatal(err)
	}
	m1 := e.mail.last()
	tok1 := m1.body[strings.Index(m1.body, "token=")+6:]
	tok1 = tok1[:strings.IndexAny(tok1, "\n ")]
	if !strings.Contains(m1.body, "http://localhost:8080/login/reset?token=") || m1.to != "staff@pl.test" {
		t.Fatalf("링크의 기준 주소는 PL_BASE_URL 이어야 함: %q", m1.body)
	}
	if e.n(t, `SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash = ?`, tok1) != 0 || e.n(t, `SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash = ?`, sha256Hex(tok1)) != 1 {
		t.Fatal("토큰 원문이 저장되었거나 해시가 없음")
	}
	// 새로 발급하면 이전 링크는 무효.
	*e.clock = e.clock.Add(time.Minute)
	if err := e.s.RequestReset(ctx, "staff@pl.test"); err != nil {
		t.Fatal(err)
	}
	m2 := e.mail.last()
	tok2 := m2.body[strings.Index(m2.body, "token=")+6:]
	tok2 = tok2[:strings.IndexAny(tok2, "\n ")]
	if err := e.s.ResetPassword(ctx, tok1, "Brand-New-Pw1!"); !errors.Is(err, ErrBadLink) {
		t.Fatalf("이전 링크가 유효함: %v", err)
	}
	if err := e.s.ResetPassword(ctx, tok2, "short"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("정책 위반: %v", err)
	}
	if err := e.s.ResetPassword(ctx, tok2, "Brand-New-Pw1!"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ResetPassword(ctx, tok2, "Another-Pw-2@"); !errors.Is(err, ErrBadLink) {
		t.Fatalf("1회용이 아님: %v", err)
	}
	var revoked sql.NullString
	_ = e.db.QueryRow(`SELECT revoked_at FROM sessions WHERE id_hash=?`, sha256Hex(lr.RawSession)).Scan(&revoked)
	if !revoked.Valid {
		t.Fatal("재설정 뒤 기존 세션이 살아 있음")
	}
	if _, err := e.login("staff@pl.test", "Brand-New-Pw1!"); err != nil {
		t.Fatalf("잠금이 풀리고 새 비밀번호로 로그인되어야 함: %v", err)
	}
}

func TestResetLinkExpiresAndRateLimited(t *testing.T) {
	e := newPwEnv(t, true)
	ctx := context.Background()
	e.setPw(t, "staff@pl.test", goodPw, false)
	_ = e.s.RequestReset(ctx, "staff@pl.test")
	m := e.mail.last()
	tok := m.body[strings.Index(m.body, "token=")+6:]
	tok = tok[:strings.IndexAny(tok, "\n ")]
	*e.clock = e.clock.Add(31 * time.Minute)
	if err := e.s.ResetPassword(ctx, tok, "Brand-New-Pw1!"); !errors.Is(err, ErrBadLink) {
		t.Fatalf("30분이 지난 링크가 유효함: %v", err)
	}
	for i := 0; i < 5; i++ {
		_ = e.s.RequestReset(ctx, "staff@pl.test")
	}
	if len(e.mail.sent) != resetPerHour {
		t.Fatalf("1시간 3건 제한: 보낸 메일 %d", len(e.mail.sent))
	}
}

// SMTP 가 없으면 재설정 요청은 거절되고(화면은 안내만), 있으면 발송 실패는 응답에 드러내지 않고 감사에 error 로 남긴다(E9).
func TestResetWithoutAndFailingMail(t *testing.T) {
	e := newPwEnv(t, false)
	if err := e.s.RequestReset(context.Background(), "staff@pl.test"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("SMTP 없이 링크 요청이 됨: %v", err)
	}
	e2 := newPwEnv(t, true)
	e2.setPw(t, "staff@pl.test", goodPw, false)
	e2.mail.fail = true
	if err := e2.s.RequestReset(context.Background(), "staff@pl.test"); err != nil {
		t.Fatalf("발송 실패가 응답에 드러남: %v", err)
	}
	if e2.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='auth.password_reset_request' AND result='error'`) != 1 {
		t.Fatal("발송 실패 감사가 없음")
	}
}

// 관리자 설정 링크(via=mail)로 비밀번호 없는 계정이 시작된다.
func TestSetupLinkStartsAccount(t *testing.T) {
	e := newPwEnv(t, true)
	ctx := context.Background()
	res, err := e.s.StartLogin(ctx, e.admin, e.uid(t, "staff@pl.test"), "mail")
	if err != nil || res.Via != "mail" {
		t.Fatalf("%+v %v", res, err)
	}
	m := e.mail.last()
	tok := m.body[strings.Index(m.body, "token=")+6:]
	tok = tok[:strings.IndexAny(tok, "\n ")]
	if err := e.s.ResetPassword(ctx, tok, goodPw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login("staff@pl.test", goodPw); err != nil {
		t.Fatal(err)
	}
	*e.clock = e.clock.Add(73 * time.Hour)
}

// 감사 기록이 실패하면 비밀번호 변경·임시 발급도 실패한다.
func TestPasswordAuditFailureFailsRequest(t *testing.T) {
	e := newPwEnv(t, false)
	e.s.audit = failRec{}
	if _, err := e.s.StartLogin(context.Background(), e.admin, e.uid(t, "staff@pl.test"), "temp"); err == nil {
		t.Fatal("감사 실패인데 임시 비밀번호가 발급됨")
	}
	if e.n(t, `SELECT COUNT(*) FROM user_credentials`) != 0 {
		t.Fatal("자격이 남음")
	}
}

type failRec struct{}

func (failRec) Record(context.Context, contract.Tx, caudit.Entry) error {
	return errors.New("disk full")
}

// 속도 제한: 분당 20회, 초과는 거부하고 감사는 10분당 1행.
func TestIPLimiter(t *testing.T) {
	l := newIPLimiter()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for i := 0; i < ipLimitPerMin; i++ {
		if ok, _ := l.allow("login", "1.2.3.4", now); !ok {
			t.Fatalf("%d번째에서 거부됨", i)
		}
	}
	ok, a1 := l.allow("login", "1.2.3.4", now)
	ok2, a2 := l.allow("login", "1.2.3.4", now.Add(time.Second))
	if ok || ok2 || !a1 || a2 {
		t.Fatalf("거부·감사 1행: %v %v %v %v", ok, ok2, a1, a2)
	}
	if ok, _ := l.allow("login", "5.6.7.8", now); !ok {
		t.Fatal("다른 IP 가 막힘")
	}
	if ok, _ := l.allow("login", "1.2.3.4", now.Add(61*time.Second)); !ok {
		t.Fatal("1분 뒤에도 막힘")
	}
	if _, a := l.allow("login", "1.2.3.4", now.Add(11*time.Minute)); a {
		// 허용된 요청이므로 감사 신호는 없다
		t.Fatal("허용된 요청에 감사 신호")
	}
}

// 동시 해시 한도 초과는 ErrBusy 로 끝나고 서버가 메모리를 소진하지 않는다(슬롯 4개).
func TestHashConcurrencyLimit(t *testing.T) {
	h := newHasher(lowArgon)
	for i := 0; i < hashSlots; i++ {
		h.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("한도 초과: %v", err)
	}
	// 변조된 해시의 거대한 파라미터는 계산하지 않는다.
	if _, _, err := h.Verify(context.Background(), "x", "$argon2id$v=19$m=4194304,t=3,p=2$c2FsdHNhbHQ$a2V5a2V5"); err == nil {
		t.Fatal("허용 범위를 넘는 파라미터를 계산함")
	}
}

// 비밀번호 로그인은 세션 방식·감사에 password 로 남는다(AC-1082).
func TestLoginRecordsMethod(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	lr, err := e.login("staff@pl.test", goodPw)
	if err != nil {
		t.Fatal(err)
	}
	var m string
	_ = e.db.QueryRow(`SELECT auth_method FROM sessions WHERE id_hash=?`, sha256Hex(lr.RawSession)).Scan(&m)
	if m != MethodPassword {
		t.Fatalf("auth_method=%q", m)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='session.login' AND result='success' AND diff LIKE '%password%'`) != 1 {
		t.Fatal("로그인 감사에 방식이 없음")
	}
}

// 로그인할 때 요청의 기존 세션은 폐기된다(고정 공격 방지: 로그인마다 새 세션 ID).
func TestLoginReplacesPreviousSession(t *testing.T) {
	e := newPwEnv(t, false)
	e.setPw(t, "staff@pl.test", goodPw, false)
	first, _ := e.login("staff@pl.test", goodPw)
	second, err := e.s.LoginPassword(context.Background(), PasswordLogin{Email: "staff@pl.test", Password: goodPw, PrevSession: first.RawSession})
	if err != nil {
		t.Fatal(err)
	}
	if second.RawSession == first.RawSession {
		t.Fatal("세션 ID 재사용")
	}
	if _, _, err := e.s.sessionPrincipal(context.Background(), first.RawSession); err == nil {
		t.Fatal("기존 세션이 살아 있음")
	}
}
