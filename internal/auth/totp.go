package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 기본값(인증 앱 호환). 비밀키 해시가 아니라 HMAC 이다.
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const (
	totpPeriod     = 30
	totpDigits     = 6
	ticketTTL      = 5 * time.Minute
	ticketAttempts = 5
	recoveryCount  = 8
	totpIssuer     = "ProcureLedger"
)

// 2단계 인증 오류.
var (
	ErrTOTPOff   = fmt.Errorf("%w: 2단계 인증이 설정되지 않았습니다(서버에 PL_AUTH_SECRET_KEY 가 없습니다). 관리자에게 문의하세요", contract.ErrInvalidState)
	ErrBadTicket = errors.New("로그인 시간이 지났거나 시도 횟수를 넘었습니다. 처음부터 다시 로그인하세요")
	ErrBadCode   = errors.New("인증 코드가 올바르지 않습니다")
)

// ---- RFC 4226/6238 ----

// hotp 는 RFC 4226 의 HOTP(HMAC-SHA1, 동적 절단)다.
func hotp(secret []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// matchTOTP 는 시계 오차 ±1 구간을 허용해 코드를 맞춰 본다. 일치한 구간이 lastStep 보다 커야 한다(같은 구간의 코드는 한 번만).
// 세 구간을 항상 모두 상수 시간으로 비교한다.
func matchTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	cur := totpStep(now)
	var hit int64
	found := 0
	for d := int64(-1); d <= 1; d++ {
		want := hotp(secret, uint64(cur+d), totpDigits)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			hit, found = cur+d, found|1
		}
	}
	if found == 0 || hit <= lastStep {
		return 0, false
	}
	return hit, true
}

// ---- 비밀키 암호화(AES-256-GCM) ----

type secretBox struct{ gcm cipher.AEAD }

func newSecretBox(key []byte) (*secretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("auth: PL_AUTH_SECRET_KEY 는 base64 로 인코딩한 32바이트여야 합니다")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &secretBox{gcm: g}, nil
}

// seal 은 nonce||암호문 을 base64 로 돌려준다. aad(사용자 번호)에 묶어 다른 사용자 행으로 옮겨 붙일 수 없다.
func (b *secretBox) seal(plain []byte, aad string) (string, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b.gcm.Seal(nonce, nonce, plain, []byte(aad))), nil
}

func (b *secretBox) open(enc, aad string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < b.gcm.NonceSize() {
		return nil, errors.New("암호문 형식")
	}
	return b.gcm.Open(nil, raw[:b.gcm.NonceSize()], raw[b.gcm.NonceSize():], []byte(aad))
}

// DecodeSecretKey 는 PL_AUTH_SECRET_KEY(base64)를 32바이트 키로 푼다. 비어 있으면 nil(기능 꺼짐).
func DecodeSecretKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if k, err = base64.RawStdEncoding.DecodeString(s); err != nil {
			return nil, errors.New("PL_AUTH_SECRET_KEY 는 base64 형식이어야 합니다")
		}
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("PL_AUTH_SECRET_KEY 는 32바이트여야 합니다(지금 %d바이트)", len(k))
	}
	return k, nil
}

// TOTPAvailable 은 2단계 인증 기능이 켜져 있는지다.
func (s *Service) TOTPAvailable() bool { return s.box != nil }

type txOrDB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) totpConfirmed(ctx context.Context, q txOrDB, userID int64) bool {
	var at sql.NullString
	err := q.QueryRowContext(ctx, `SELECT confirmed_at FROM user_totp WHERE user_id = ?`, userID).Scan(&at)
	return err == nil && at.Valid
}

// totpSecret 는 활성 비밀키를 푼다.
func (s *Service) totpSecret(ctx context.Context, q txOrDB, userID int64) (secret []byte, lastStep int64, err error) {
	var enc sql.NullString
	if err = q.QueryRowContext(ctx, `SELECT secret_enc, last_step FROM user_totp WHERE user_id = ? AND confirmed_at IS NOT NULL`, userID).Scan(&enc, &lastStep); err != nil {
		return nil, 0, err
	}
	secret, err = s.box.open(enc.String, idStr(userID))
	return secret, lastStep, err
}

func normalizeCode(c string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(c))
}

func isTOTPCode(c string) bool {
	if len(c) != totpDigits {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func newRecoveryCode() (string, error) {
	b := make([]byte, 6) // 48비트 → base32 10자
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	c := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:10]
	return c[:5] + "-" + c[5:], nil
}

func recoveryHash(code string) string {
	return sha256Hex("pl-recovery-v1:" + normalizeCode(strings.ToLower(code)))
}

// ---- 등록·해제 ----

// EnrollTOTP 는 새 비밀키를 만들어 등록 중(pending)으로 저장하고 키 텍스트와 otpauth 링크를 돌려준다(QR 은 뒤로 미룸: D6).
// 이미 활성이면 현재 비밀번호와 코드가 필요하다(재등록). 확인(ConfirmTOTP)하기 전에는 기존 설정이 그대로 유효하다.
func (s *Service) EnrollTOTP(ctx context.Context, p contract.Principal, password, code string) (secretB32, uri string, err error) {
	if s.box == nil {
		return "", "", ErrTOTPOff
	}
	email, _, err := s.userEmailName(ctx, p.UserID)
	if err != nil {
		return "", "", err
	}
	if s.totpConfirmed(ctx, s.db, p.UserID) {
		if err := s.verifyPasswordAndCode(ctx, p.UserID, password, code); err != nil {
			return "", "", err
		}
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	enc, err := s.box.seal(raw, idStr(p.UserID))
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC().Format(timeLayout)
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_totp (user_id, pending_secret_enc, created_at) VALUES (?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET pending_secret_enc = excluded.pending_secret_enc`, p.UserID, enc, now); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.totp_enroll", audit.TargetCredential, idStr(p.UserID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "enroll_stage", New: "begin"}}, "")
	})
	if err != nil {
		return "", "", err
	}
	secretB32 = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	v := url.Values{"secret": {secretB32}, "issuer": {totpIssuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return secretB32, "otpauth://totp/" + url.PathEscape(totpIssuer+":"+email) + "?" + v.Encode(), nil
}

// verifyPasswordAndCode 는 재등록·해제 때 현재 비밀번호와 2단계 코드를 확인한다(둘 다 틀림 횟수에 합산).
func (s *Service) verifyPasswordAndCode(ctx context.Context, userID int64, password, code string) error {
	c, err := scanCred(s.db.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: 비밀번호가 없는 계정입니다", contract.ErrInvalidState)
	}
	if err != nil {
		return err
	}
	if c.lockedUntil != nil && s.now().UTC().Before(*c.lockedUntil) {
		return LockedError{*c.lockedUntil}
	}
	ok, _, err := s.hasher.Verify(ctx, password, c.hash)
	if err != nil {
		return err
	}
	var step int64
	if ok {
		secret, last, serr := s.totpSecret(ctx, s.db, userID)
		if serr != nil {
			return serr
		}
		step, ok = matchTOTP(secret, normalizeCode(code), s.now(), last)
	}
	if !ok {
		if ferr := s.recordFailure(ctx, userID); ferr != nil && !errors.Is(ferr, ErrBadCredentials) {
			return ferr
		}
		return fmt.Errorf("%w: 현재 비밀번호 또는 인증 코드가 올바르지 않습니다", contract.ErrValidation)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
	return err
}

// ConfirmTOTP 는 인증 앱의 코드를 확인해 등록을 활성화하고 복구 코드 8개를 돌려준다(원문은 이 응답에서만).
// 필수 역할로 제한된 세션이었다면 이 세션의 제한이 풀린다.
func (s *Service) ConfirmTOTP(ctx context.Context, p contract.Principal, rawSession, code string) ([]string, error) {
	if s.box == nil {
		return nil, ErrTOTPOff
	}
	var pending sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT pending_secret_enc FROM user_totp WHERE user_id = ?`, p.UserID).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !pending.Valid) {
		return nil, fmt.Errorf("%w: 먼저 2단계 인증 등록을 시작하세요", contract.ErrInvalidState)
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.box.open(pending.String, idStr(p.UserID))
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, normalizeCode(code), s.now(), 0)
	if !ok {
		return nil, fmt.Errorf("%w: 인증 앱의 코드가 맞지 않습니다. 앱의 시계와 입력한 코드를 확인하세요", contract.ErrValidation)
	}
	codes := make([]string, recoveryCount)
	for i := range codes {
		if codes[i], err = newRecoveryCode(); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC().Format(timeLayout)
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE user_totp SET secret_enc = pending_secret_enc, pending_secret_enc = NULL, confirmed_at = ?, last_step = ? WHERE user_id = ?`, now, step, p.UserID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, p.UserID); err != nil {
			return err
		}
		for _, c := range codes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES (?,?)`, p.UserID, recoveryHash(c)); err != nil {
				return err
			}
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
		return s.record(ctx, tx, p, "auth.totp_enroll", audit.TargetCredential, idStr(p.UserID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "enroll_stage", New: "confirm"}, {Key: "recovery_codes", New: fmt.Sprint(recoveryCount)}}, "")
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP 는 본인이 2단계 인증을 해제한다(현재 비밀번호+코드). 필수 역할·비상 관리자는 해제할 수 없다.
func (s *Service) DisableTOTP(ctx context.Context, p contract.Principal, password, code string) error {
	if s.box == nil {
		return ErrTOTPOff
	}
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return err
	}
	if pol.totpRequiredFor(ctx, s.db, p.UserID) {
		return fmt.Errorf("%w: 이 계정은 2단계 인증이 필수라 해제할 수 없습니다. 기기를 바꾸려면 재등록하세요", contract.ErrValidation)
	}
	if !s.totpConfirmed(ctx, s.db, p.UserID) {
		return fmt.Errorf("%w: 2단계 인증을 쓰고 있지 않습니다", contract.ErrInvalidState)
	}
	if err := s.verifyPasswordAndCode(ctx, p.UserID, password, code); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.deleteTOTP(ctx, tx, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.totp_disable", audit.TargetCredential, idStr(p.UserID), audit.ResultSuccess, nil, "")
	})
}

func (s *Service) deleteTOTP(ctx context.Context, tx contract.Tx, userID int64) error {
	for _, q := range []string{`DELETE FROM user_totp WHERE user_id = ?`, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, `DELETE FROM login_tickets WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return nil
}

// ResetTOTP 는 관리자가 사용자의 2단계 인증을 초기화한다(기기 분실). 그 사용자의 세션을 모두 폐기한다.
// 필수 역할 사용자는 다음 로그인에서 재등록이 강제된다(pendingTOTPSetup).
func (s *Service) ResetTOTP(ctx context.Context, p contract.Principal, userID int64) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermAuthManage); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_totp WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return contract.ErrNotFound
		}
		if err := s.deleteTOTP(ctx, tx, userID); err != nil {
			return err
		}
		if err := s.revokeUserSessions(ctx, tx, userID, ""); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "auth.totp_reset", audit.TargetCredential, idStr(userID), audit.ResultSuccess, nil, "")
	})
}

// ---- 로그인 티켓 ----

func (s *Service) issueTicket(ctx context.Context, tx contract.Tx, userID int64, method, prevRaw string) (string, error) {
	raw, err := randomString(32)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_tickets WHERE expires_at < ? OR user_id = ?`, now.Format(timeLayout), userID); err != nil {
		return "", err
	}
	var prev any
	if prevRaw != "" {
		prev = sha256Hex(prevRaw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO login_tickets (ticket_hash, user_id, method, prev_hash, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
		sha256Hex(raw), userID, method, prev, now.Format(timeLayout), now.Add(ticketTTL).Format(timeLayout))
	return raw, err
}

// LoginTOTP 는 티켓과 코드(인증 앱 6자리 또는 복구 코드)로 로그인을 마친다. 코드가 틀리면 티켓 시도와 계정 잠금 횟수가 함께 오른다.
func (s *Service) LoginTOTP(ctx context.Context, ticket, code string) (LoginResult, error) {
	if s.box == nil {
		return LoginResult{}, ErrTOTPOff
	}
	code = normalizeCode(code)
	var res LoginResult
	var outcome error
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var userID int64
		var method, exp string
		emergency := false
		var prev sql.NullString
		var attempts int
		err := tx.QueryRowContext(ctx, `SELECT user_id, method, prev_hash, expires_at, attempts FROM login_tickets WHERE ticket_hash = ?`, sha256Hex(ticket)).
			Scan(&userID, &method, &prev, &exp, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = ErrBadTicket
			return nil
		}
		if err != nil {
			return err
		}
		if m, ok := strings.CutSuffix(method, "+emergency"); ok {
			method, emergency = m, true
		}
		now := s.now().UTC()
		if attempts >= ticketAttempts || !now.Before(parseTime(exp)) {
			outcome = ErrBadTicket
			return nil
		}
		c, err := scanCred(tx.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID))
		if err != nil {
			return err
		}
		if c.lockedUntil != nil && now.Before(*c.lockedUntil) {
			outcome = LockedError{*c.lockedUntil}
			return nil
		}
		ok, factor := s.checkSecondFactor(ctx, tx, userID, code, now)
		if !ok {
			if _, err := tx.ExecContext(ctx, `UPDATE login_tickets SET attempts = attempts + 1 WHERE ticket_hash = ?`, sha256Hex(ticket)); err != nil {
				return err
			}
			if outcome, err = s.failureTx(ctx, tx, userID); err != nil {
				return err
			}
			if errors.Is(outcome, ErrBadCredentials) {
				outcome = ErrBadCode
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_tickets WHERE ticket_hash = ?`, sha256Hex(ticket)); err != nil {
			return err
		}
		u, err := s.resolveUserID(ctx, tx, userID)
		if err != nil {
			if errors.Is(err, contract.ErrForbidden) {
				if rerr := s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, "session.login", audit.TargetSession, "user:"+idStr(userID),
					audit.ResultDenied, []audit.DiffItem{{Key: "login_method", New: method}, {Key: "reason", New: denialReason(err)}}, "재직 중이 아니거나 비활성인 계정"); rerr != nil {
					return rerr
				}
				outcome = ErrBadTicket
				return nil
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_credentials SET failed_count = 0, locked_until = NULL WHERE user_id = ?`, userID); err != nil {
			return err
		}
		setup, err := s.pendingSetup(ctx, tx, userID, string(u.Role))
		if err != nil {
			return err
		}
		if prev.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id_hash = ? AND user_id = ? AND revoked_at IS NULL`, now.Format(timeLayout), prev.String, userID); err != nil {
				return err
			}
		}
		raw, expires, prefix, err := s.createSession(ctx, tx, userID, method, setup)
		if err != nil {
			return err
		}
		res = LoginResult{RawSession: raw, Expires: expires, Setup: setup}
		if emergency {
			if err := s.record(ctx, tx, u, "auth.emergency_login", audit.TargetCredential, idStr(userID), audit.ResultSuccess, nil, "비상 로그인 사용(경고)"); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, u, "session.login", audit.TargetSession, prefix, audit.ResultSuccess,
			[]audit.DiffItem{{Key: "login_method", New: method}, {Key: "second_factor", New: factor}}, "")
	})
	if err != nil {
		return LoginResult{}, err
	}
	if outcome != nil {
		return LoginResult{}, outcome
	}
	return res, nil
}

// checkSecondFactor 는 코드가 인증 앱 코드(재생 방지)이거나 쓰지 않은 복구 코드인지 확인하고 소비한다.
func (s *Service) checkSecondFactor(ctx context.Context, tx contract.Tx, userID int64, code string, now time.Time) (ok bool, factor string) {
	if isTOTPCode(code) {
		secret, last, err := s.totpSecret(ctx, tx, userID)
		if err != nil {
			return false, ""
		}
		step, hit := matchTOTP(secret, code, now, last)
		if !hit {
			return false, ""
		}
		res, err := tx.ExecContext(ctx, `UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
		if err != nil {
			return false, ""
		}
		n, _ := res.RowsAffected()
		return n == 1, "totp"
	}
	if code == "" {
		return false, ""
	}
	res, err := tx.ExecContext(ctx, `UPDATE totp_recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		now.Format(timeLayout), userID, recoveryHash(code))
	if err != nil {
		return false, ""
	}
	n, _ := res.RowsAffected()
	return n == 1, "recovery"
}

// authSummary 는 /api/me 가 화면에 알리는 로그인 수단 요약이다(내 계정의 2단계 인증 화면용). 비밀은 없다.
func (s *Service) authSummary(ctx context.Context, p contract.Principal) map[string]any {
	out := map[string]any{"method": "", "hasPassword": false, "totpAvailable": s.box != nil, "totpEnrolled": false, "totpRequired": false}
	if raw, ok := ctx.Value(rawSessionKey{}).(string); ok && raw != "" {
		var method string
		_ = s.db.QueryRowContext(ctx, `SELECT auth_method FROM sessions WHERE id_hash = ?`, sha256Hex(raw)).Scan(&method)
		out["method"] = method
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_credentials WHERE user_id = ?`, p.UserID).Scan(&n)
	out["hasPassword"] = n > 0
	if s.box != nil {
		out["totpEnrolled"] = s.totpConfirmed(ctx, s.db, p.UserID)
		if pol, err := loadPolicy(ctx, s.db); err == nil {
			out["totpRequired"] = pol.totpRequiredFor(ctx, s.db, p.UserID)
		}
	}
	return out
}

type rawSessionKey struct{}
