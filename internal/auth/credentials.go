package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const (
	maxFailures     = 5
	lockDuration    = 15 * time.Minute
	tempPasswordTTL = 24 * time.Hour
	setupLinkTTL    = 72 * time.Hour
	resetLinkTTL    = 30 * time.Minute
	resetPerHour    = 3
	historyKeep     = 10
)

// 비밀번호 로그인 오류. 계정 존재 여부를 드러내지 않도록 문구는 한 가지다.
var (
	ErrBadCredentials = errors.New("이메일 또는 비밀번호가 올바르지 않습니다")
	ErrEmailLoginOff  = errors.New("이메일 로그인이 꺼져 있습니다. 관리자에게 문의하세요")
)

// LockedError 는 계정이 잠겨 있다는 뜻이다(D3: 맞는 비밀번호든 아니든 같은 문구).
type LockedError struct{ Until time.Time }

func (e LockedError) Error() string {
	return fmt.Sprintf("로그인 시도가 너무 많아 15분 동안 잠겼습니다. %s 이후에 다시 시도하거나 관리자에게 해제를 요청하세요", e.Until.In(kst).Format("15:04"))
}

var kst = time.FixedZone("KST", 9*3600)

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AuthPolicy 는 인증 정책 전체(auth_policy 한 행)다. 빈 항목은 기본값이다.
type AuthPolicy struct {
	Password          PasswordPolicy `json:"password"`
	EmailLoginEnabled bool           `json:"emailLoginEnabled"`
	// TOTPRequiredPerms: 이 권한 중 하나라도 실효 권한으로 가진 사용자는 2단계 인증이 필수다(역할 이름이 아니라 권한 기준, B33).
	TOTPRequiredPerms []string `json:"totpRequiredPerms"`
	EmergencyAdmins   []int64  `json:"emergencyAdmins"`
}

// DefaultAuthPolicy 는 정책 기본값이다(역할·인증 설정을 바꿀 수 있는 사람, 곧 기본 관리자 역할은 2단계 필수).
func DefaultAuthPolicy() AuthPolicy {
	return AuthPolicy{Password: DefaultPasswordPolicy(), EmailLoginEnabled: true, TOTPRequiredPerms: []string{string(contract.PermRoleManage), string(contract.PermAuthManage)}, EmergencyAdmins: []int64{}}
}

// totpRequiredFor 는 이 사용자에게 2단계 인증이 필수인지다(필수 권한 보유자이거나 비상 관리자). 실효 권한 캐시로 판정한다.
func (p AuthPolicy) totpRequiredFor(ctx context.Context, q queryer, userID int64) bool {
	for _, id := range p.EmergencyAdmins {
		if id == userID {
			return true
		}
	}
	for _, perm := range p.TOTPRequiredPerms {
		if ok, err := access.HasPerm(ctx, q, userID, contract.Permission(perm)); err == nil && ok {
			return true
		}
	}
	return false
}

func loadPolicy(ctx context.Context, q queryer) (AuthPolicy, error) {
	p := DefaultAuthPolicy()
	var raw string
	err := q.QueryRowContext(ctx, `SELECT config_json FROM auth_policy WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.TOTPRequiredPerms = nil // 저장된 값이 있는지 가려내기 위해 기본값을 비운다(없으면 이전 형식 이관 또는 아래 기본값)
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DefaultAuthPolicy(), nil // 깨진 정책은 기본값으로(fail-safe: 더 엄격한 쪽)
	}
	if p.TOTPRequiredPerms == nil {
		// 이관: B33 이전 정책은 역할 이름 목록(totpRequiredRoles)이었다. 그 기본 역할들이 가진 위험 권한 중 하나라도 가진 사람이 필수 대상이 되도록 바꾼다.
		var legacy struct {
			Roles *[]string `json:"totpRequiredRoles"`
		}
		_ = json.Unmarshal([]byte(raw), &legacy)
		if legacy.Roles == nil { // 둘 다 없으면 기본값
			p.TOTPRequiredPerms = DefaultAuthPolicy().TOTPRequiredPerms
		} else if r := *legacy.Roles; len(r) == 1 && r[0] == "admin" {
			// 옛 기본값(["admin"])은 새 기본값(역할 관리·로그인 설정 권한 보유자)으로 바꾼다. 재무 같은 다른 역할이 갑자기 2단계 필수가 되지 않는다.
			p.TOTPRequiredPerms = DefaultAuthPolicy().TOTPRequiredPerms
		} else {
			p.TOTPRequiredPerms = legacyPerms(r)
		}
	}
	if p.EmergencyAdmins == nil {
		p.EmergencyAdmins = []int64{}
	}
	return p, nil
}

// legacyPerms 는 이전 형식의 필수 역할 목록을 권한 목록으로 옮긴다: 각 기본 역할 템플릿이 가진 위험 권한의 합집합.
// 역할 목록이 비어 있었으면(아무도 필수 아님) 빈 목록 그대로다.
func legacyPerms(roles []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range roles {
		for _, perm := range contract.RolePermissions[contract.Role(r)] {
			for _, risk := range contract.HighRiskPermissions {
				if perm == risk && !seen[string(perm)] {
					seen[string(perm)] = true
					out = append(out, string(perm))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// Policy 는 현재 인증 정책이다.
func (s *Service) Policy(ctx context.Context) (AuthPolicy, error) { return loadPolicy(ctx, s.db) }

type credRow struct {
	userID      int64
	hash        string
	mustChange  bool
	expiresAt   *time.Time
	failed      int
	lockedUntil *time.Time
	tempExpires *time.Time
}

func scanCred(r rowScanner) (credRow, error) {
	var c credRow
	var must int
	var exp, lock, temp sql.NullString
	err := r.Scan(&c.userID, &c.hash, &must, &exp, &c.failed, &lock, &temp)
	c.mustChange = must == 1
	c.expiresAt, c.lockedUntil, c.tempExpires = parseTimePtr(exp), parseTimePtr(lock), parseTimePtr(temp)
	return c, err
}

const credSelect = `SELECT c.user_id, c.password_hash, c.must_change, c.expires_at, c.failed_count, c.locked_until, c.temp_expires_at FROM user_credentials c`

// dummy 해시: 존재하지 않는 계정에도 같은 비용의 검증을 해 소요 시간으로 계정 유무를 알 수 없게 한다.
// 실제 자격과 같은 설정 비용(Options.Argon)으로 기동할 때 한 번 계산해 두고(백그라운드), 첫 요청이 그 계산을 기다린다.
// 저장된 해시가 설정보다 낮은 비용이면(재해시 대기) 실제 검증이 그만큼 빠르다. 그 차이는 재해시가 끝나면 사라진다.
func (s *Service) startDummyHash() {
	s.dummyDone = make(chan struct{})
	go func() {
		defer close(s.dummyDone)
		h, err := s.hasher.Hash(context.Background(), "dummy-password-for-timing")
		if err == nil {
			s.dummyMu.Lock()
			s.dummyHash = h
			s.dummyMu.Unlock()
		}
	}()
}

func (s *Service) dummyVerify(ctx context.Context, password string) {
	if s.dummyDone != nil {
		<-s.dummyDone
	}
	s.dummyMu.Lock()
	h := s.dummyHash
	if h == "" {
		h, _ = s.hasher.Hash(ctx, "dummy-password-for-timing")
		s.dummyHash = h
	}
	s.dummyMu.Unlock()
	_, _, _ = s.hasher.Verify(ctx, password, h)
}

// PasswordLogin 은 비밀번호 로그인 입력이다.
type PasswordLogin struct {
	Email       string
	Password    string
	PrevSession string // 요청 쿠키의 기존 세션(있으면 새 세션을 만들면서 폐기한다)
	// Emergency 는 비상 로그인(/login/emergency)이다: 이메일 로그인이 꺼져 있어도 비상 관리자만, 2단계 인증과 함께 들어올 수 있다.
	Emergency bool
}

// LoginResult 는 로그인 성공 결과다. Setup 이 비어 있지 않으면 제한 세션이다.
type LoginResult struct {
	RawSession string
	Expires    time.Time
	Setup      string
	// Ticket 이 비어 있지 않으면 비밀번호는 통과했고 2단계 코드가 남았다(세션 없음). LoginTOTP 로 마무리한다.
	Ticket string
}

// LoginPassword 는 이메일+비밀번호 로그인(명세 4.4)이다. 없는 계정·비밀번호 없는 계정·틀린 비밀번호·퇴사 계정은 모두
// ErrBadCredentials 로 같은 비용을 들여 돌려준다. 잠금은 계정별 연속 실패 5회 15분이다.
func (s *Service) LoginPassword(ctx context.Context, in PasswordLogin) (LoginResult, error) {
	email := normEmail(in.Email)
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return LoginResult{}, err
	}
	if !pol.EmailLoginEnabled && !in.Emergency {
		return LoginResult{}, ErrEmailLoginOff
	}
	if email == "" || in.Password == "" || len(in.Password) > 1024 {
		s.dummyVerify(ctx, in.Password)
		return LoginResult{}, ErrBadCredentials
	}
	c, err := scanCred(s.db.QueryRowContext(ctx, credSelect+` JOIN users u ON u.id = c.user_id WHERE lower(u.email) = ?`, email))
	if errors.Is(err, sql.ErrNoRows) {
		s.dummyVerify(ctx, in.Password)
		return LoginResult{}, ErrBadCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	now := s.now().UTC()
	// 비활성·퇴사 계정은 없는 이메일과 똑같이 다룬다: 잠금 행을 만들지 않고 잠금 응답(423)도 내지 않는다(계정 존재 노출 금지).
	_, aerr := s.resolveUserID(ctx, s.db, c.userID)
	unusable := errors.Is(aerr, contract.ErrForbidden)
	if aerr != nil && !unusable {
		return LoginResult{}, aerr
	}
	// 비상 로그인은 비상 관리자(2단계 인증 등록 완료)만 쓸 수 있다. 아니면 없는 계정과 같은 응답이다.
	if in.Emergency && !unusable && !s.isEmergencyAdmin(ctx, pol, c.userID) {
		unusable = true
	}
	if !unusable && c.lockedUntil != nil && now.Before(*c.lockedUntil) {
		return LoginResult{}, LockedError{*c.lockedUntil}
	}
	ok, rehash, verr := s.hasher.Verify(ctx, in.Password, c.hash)
	if verr != nil {
		return LoginResult{}, verr
	}
	if unusable {
		if ok && !in.Emergency {
			s.auditUnusable(ctx, c.userID, aerr)
		}
		return LoginResult{}, ErrBadCredentials
	}
	if !ok {
		return LoginResult{}, s.recordFailure(ctx, c.userID)
	}
	newHash := ""
	if rehash {
		if newHash, err = s.hasher.Hash(ctx, in.Password); err != nil {
			return LoginResult{}, err
		}
	}
	var res LoginResult
	denied := false
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := scanCred(tx.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, c.userID))
		if err != nil {
			return err
		}
		if cur.lockedUntil != nil && now.Before(*cur.lockedUntil) { // 검증하는 사이에 잠겼다
			return LockedError{*cur.lockedUntil}
		}
		if cur.hash != c.hash || (cur.tempExpires != nil && !now.Before(*cur.tempExpires)) {
			denied = true // 그사이 비밀번호가 바뀌었거나 임시 비밀번호가 만료됐다
			return nil
		}
		u, err := s.resolveUserID(ctx, tx, c.userID)
		if err != nil {
			if errors.Is(err, contract.ErrForbidden) {
				if rerr := s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "session.login", audit.TargetSession, "user:"+idStr(c.userID),
					audit.ResultDenied, []audit.DiffItem{{Key: "login_method", New: MethodPassword}, {Key: "reason", New: denialReason(err)}}, "재직 중이 아니거나 비활성인 계정"); rerr != nil {
					return rerr
				}
				denied = true
				return nil
			}
			return err
		}
		if newHash != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET password_hash = ? WHERE user_id = ? AND password_hash = ?`, newHash, c.userID, c.hash); err != nil {
				return err
			}
		}
		if s.box != nil && s.totpConfirmed(ctx, tx, c.userID) {
			// 2단계 인증 등록자: 세션 대신 로그인 티켓을 준다. 실패 횟수는 2단계까지 통과해야 0 이 된다(비밀번호만 맞히고 코드를 찍어 보는 우회 방지).
			tm := MethodPassword
			if in.Emergency {
				tm += "+emergency" // 티켓을 마무리할 때 비상 로그인 감사를 남긴다
			}
			t, err := s.issueTicket(ctx, tx, c.userID, tm, in.PrevSession)
			res = LoginResult{Ticket: t}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = 0, locked_until = NULL WHERE user_id = ?`, c.userID); err != nil {
			return err
		}
		setup, err := s.pendingSetup(ctx, tx, c.userID, string(u.Role))
		if err != nil {
			return err
		}
		if err := s.revokeSessionRaw(ctx, tx, c.userID, in.PrevSession); err != nil {
			return err
		}
		raw, exp, prefix, err := s.createSession(ctx, tx, c.userID, MethodPassword, setup)
		if err != nil {
			return err
		}
		res = LoginResult{RawSession: raw, Expires: exp, Setup: setup}
		return s.record(ctx, tx, u, "session.login", audit.TargetSession, prefix, audit.ResultSuccess,
			[]audit.DiffItem{{Key: "login_method", New: MethodPassword}}, "")
	})
	if err != nil {
		return LoginResult{}, err
	}
	if denied {
		return LoginResult{}, ErrBadCredentials
	}
	return res, nil
}

// recordFailure 는 연속 실패를 한 트랜잭션에서 올리고 5회째에 잠근다(잠금 발생에만 감사 1행, 실패마다 행을 만들지 않는다).
// 잠금 중에는 횟수를 늘리지 않고 만료 시각을 연장하지 않는다.
func (s *Service) recordFailure(ctx context.Context, userID int64) error {
	var out error
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var err error
		out, err = s.failureTx(ctx, tx, userID)
		return err
	})
	if err != nil {
		return err
	}
	return out
}

// failureTx 는 recordFailure 의 트랜잭션 안 본체다(2단계 코드 실패도 같은 횟수에 합산한다). outcome 은 호출자에게 돌려줄 오류다.
func (s *Service) failureTx(ctx context.Context, tx contract.Tx, userID int64) (outcome error, err error) {
	c, err := scanCred(tx.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID))
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if c.lockedUntil != nil && now.Before(*c.lockedUntil) {
		return LockedError{*c.lockedUntil}, nil
	}
	n := c.failed + 1
	if n < maxFailures {
		_, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = ? WHERE user_id = ?`, n, userID)
		return ErrBadCredentials, err
	}
	until := now.Add(lockDuration)
	if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = 0, locked_until = ? WHERE user_id = ?`, until.Format(timeLayout), userID); err != nil {
		return nil, err
	}
	err = s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "auth.account_lock", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
		[]audit.DiffItem{{Key: "until", New: until.Format(timeLayout)}}, "")
	return LockedError{until}, err
}

// pendingSetup 은 로그인 직후 남은 제한 세션 사유를 돌려준다(없으면 ""). 3단계에서 2단계 인증 등록이 더해진다.
func (s *Service) pendingSetup(ctx context.Context, tx contract.Tx, userID int64, role string) (string, error) {
	c, err := scanCred(tx.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if c.mustChange || (c.expiresAt != nil && !s.now().UTC().Before(*c.expiresAt)) {
		return contract.SetupPasswordChange, nil
	}
	return s.pendingTOTPSetup(ctx, tx, userID)
}

// pendingTOTPSetup 은 필수 권한 보유자(또는 비상 관리자)가 2단계 인증 필수인데 등록하지 않았으면 totp_enroll 을 돌려준다.
// PL_AUTH_SECRET_KEY 가 없으면 2단계 기능 자체가 꺼져 있어 요구하지 않는다.
func (s *Service) pendingTOTPSetup(ctx context.Context, tx contract.Tx, userID int64) (string, error) {
	if s.box == nil {
		return "", nil
	}
	pol, err := loadPolicy(ctx, tx)
	if err != nil {
		return "", err
	}
	if !pol.totpRequiredFor(ctx, tx, userID) || s.totpConfirmed(ctx, tx, userID) {
		return "", nil
	}
	return contract.SetupTOTPEnroll, nil
}

func (s *Service) revokeSessionRaw(ctx context.Context, tx contract.Tx, userID int64, raw string) error {
	if raw == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id_hash = ? AND user_id = ? AND revoked_at IS NULL`,
		s.now().UTC().Format(timeLayout), sha256Hex(raw), userID)
	return err
}

// revokeUserSessions 는 사용자의 세션을 모두 폐기한다(exceptHash 는 남길 세션).
func (s *Service) revokeUserSessions(ctx context.Context, tx contract.Tx, userID int64, exceptHash string) error {
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL AND id_hash <> ?`,
		s.now().UTC().Format(timeLayout), userID, exceptHash)
	return err
}

// checkReuse 는 새 비밀번호가 현재·최근 비밀번호와 같은지 본다(정책 HistoryCount 개).
func (s *Service) checkReuse(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, userID int64, currentHash, password string, n int) error {
	if n <= 0 {
		return nil
	}
	hashes := []string{currentHash}
	if n > 1 {
		rows, err := q.QueryContext(ctx, `SELECT password_hash FROM password_history WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, n-1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return err
			}
			hashes = append(hashes, h)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if ok, _, err := s.hasher.Verify(ctx, password, h); err != nil {
			return err
		} else if ok {
			return PolicyError{fmt.Sprintf("최근 사용한 %d개의 비밀번호는 다시 쓸 수 없습니다", n)}
		}
	}
	return nil
}

func (s *Service) userEmailName(ctx context.Context, userID int64) (email, name string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT email, name FROM users WHERE id = ?`, userID).Scan(&email, &name)
	return
}

// expiryFor 는 정책의 만료 일수에서 새 비밀번호의 만료 시각을 계산한다(끄면 nil).
func (s *Service) expiryFor(pp PasswordPolicy) any {
	if pp.ExpireDays <= 0 {
		return nil
	}
	return s.now().UTC().AddDate(0, 0, pp.ExpireDays).Format(timeLayout)
}

// ChangePassword 는 로그인한 사용자가 자기 비밀번호를 바꾼다(제한 세션에서도 허용). 현재 비밀번호 확인이 필요하고
// 틀리면 잠금 횟수에 합산된다. 성공하면 다른 세션을 모두 폐기하고 이 세션의 제한을 푼다(남은 사유가 없으면).
func (s *Service) ChangePassword(ctx context.Context, p contract.Principal, rawSession, current, next string) error {
	if p.UserID == 0 {
		return contract.ErrUnauthenticated
	}
	c, err := scanCred(s.db.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, p.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 비밀번호가 설정되지 않은 계정입니다. 관리자에게 로그인 시작을 요청하세요", contract.ErrInvalidState)
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if c.lockedUntil != nil && now.Before(*c.lockedUntil) {
		return LockedError{*c.lockedUntil}
	}
	ok, _, err := s.hasher.Verify(ctx, current, c.hash)
	if err != nil {
		return err
	}
	if !ok {
		if ferr := s.recordFailure(ctx, p.UserID); ferr != nil && !errors.Is(ferr, ErrBadCredentials) {
			return ferr
		}
		return fmt.Errorf("%w: 현재 비밀번호가 올바르지 않습니다", contract.ErrValidation)
	}
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return err
	}
	email, name, err := s.userEmailName(ctx, p.UserID)
	if err != nil {
		return err
	}
	if err := pol.Password.Check(next, email, name); err != nil {
		return err
	}
	if err := s.checkReuse(ctx, s.db, p.UserID, c.hash, next, pol.Password.HistoryCount); err != nil {
		return err
	}
	newHash, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.setPassword(ctx, tx, p.UserID, newHash, c.hash, pol.Password, false); err != nil {
			return err
		}
		if err := s.revokeUserSessions(ctx, tx, p.UserID, sha256Hex(rawSession)); err != nil {
			return err
		}
		setup, err := s.pendingSetup(ctx, tx, p.UserID, string(p.Role))
		if err != nil {
			return err
		}
		var sv any
		if setup != "" {
			sv = setup
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET setup = ? WHERE id_hash = ? AND user_id = ?`, sv, sha256Hex(rawSession), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.password_change", audit.TargetCredential, idStr(p.UserID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "forced", New: fmt.Sprint(c.mustChange)}}, "")
	})
}

// setPassword 는 새 해시를 저장한다: 이전 해시를 이력에 넣고, 변경 강제·잠금·실패 횟수·임시 기한을 푼다. oldHash 가 빈 문자열이면 첫 설정이다.
// mustChange 가 참이면 다음 로그인에서 변경을 강제한다(임시 비밀번호).
func (s *Service) setPassword(ctx context.Context, tx contract.Tx, userID int64, newHash, oldHash string, pp PasswordPolicy, mustChange bool) error {
	now := s.now().UTC().Format(timeLayout)
	must := 0
	if mustChange {
		must = 1
	}
	if oldHash == "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_credentials (user_id, password_hash, must_change, changed_at, expires_at) VALUES (?,?,?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET password_hash=excluded.password_hash, must_change=excluded.must_change, changed_at=excluded.changed_at,
			expires_at=excluded.expires_at, failed_count=0, locked_until=NULL, temp_expires_at=NULL`, userID, newHash, must, now, s.expiryFor(pp)); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO password_history (user_id, password_hash, created_at) VALUES (?,?,?)`, userID, oldHash, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM password_history WHERE user_id = ? AND id NOT IN (SELECT id FROM password_history WHERE user_id = ? ORDER BY id DESC LIMIT ?)`,
			userID, userID, historyKeep); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET password_hash = ?, must_change = ?, changed_at = ?, expires_at = ?, failed_count = 0, locked_until = NULL, temp_expires_at = NULL WHERE user_id = ?`,
			newHash, must, now, s.expiryFor(pp), userID); err != nil {
			return err
		}
	}
	return nil
}

// isEmergencyAdmin 은 정책에 지정된 활성 관리자이면서 2단계 인증 등록을 마친 사용자인지다.
func (s *Service) isEmergencyAdmin(ctx context.Context, pol AuthPolicy, userID int64) bool {
	if s.box == nil {
		return false
	}
	for _, id := range pol.EmergencyAdmins {
		if id == userID {
			// 비상 관리자는 로그인 설정을 관리할 수 있는(auth:manage) 사용자여야 한다.
			if ok, err := access.HasPerm(ctx, s.db, userID, contract.PermAuthManage); err != nil || !ok {
				return false
			}
			return s.totpConfirmed(ctx, s.db, userID)
		}
	}
	return false
}

// auditUnusable 는 비활성·퇴사 계정이 정답 비밀번호로 시도한 로그인을 거부로 남긴다(감사 실패는 응답에 영향을 주지 않는다: 어차피 거부).
func (s *Service) auditUnusable(ctx context.Context, userID int64, why error) {
	_ = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "session.login", audit.TargetSession, "user:"+idStr(userID),
			audit.ResultDenied, []audit.DiffItem{{Key: "login_method", New: MethodPassword}, {Key: "reason", New: denialReason(why)}}, "재직 중이 아니거나 비활성인 계정")
	})
}
