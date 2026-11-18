package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const (
	sessionCookie  = "pl_session"
	sessionTTL     = 12 * time.Hour // 절대 만료
	sessionIdleTTL = 2 * time.Hour  // 마지막 사용 후 유휴 만료
	touchEvery     = time.Minute
)

// csrfTokenFor 는 세션 원문 ID 에서 CSRF 토큰을 유도한다. 세션 ID 는 HttpOnly 쿠키라 다른 출처가 알 수 없고,
// 토큰은 GET /api/me 응답(같은 출처만 읽을 수 있음)으로만 전달된다. DB 에는 이 토큰의 해시만 둔다.
func csrfTokenFor(rawSession string) string { return sha256Hex("pl-csrf-v1:" + rawSession) }

// createSession 은 tx 안에서 세션을 만든다. 원문 세션 ID 는 쿠키로만 나간다.
// method 는 로그인 방식(Method*), setup 은 제한 세션 사유(""=정상, contract.Setup*)다.
func (s *Service) createSession(ctx context.Context, tx contract.Tx, userID int64, method, setup string) (raw string, expires time.Time, hashPrefix string, err error) {
	raw, err = randomString(32)
	if err != nil {
		return "", time.Time{}, "", err
	}
	now := s.now().UTC()
	expires = now.Add(sessionTTL)
	idHash := sha256Hex(raw)
	var setupVal any
	if setup != "" {
		setupVal = setup
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions (id_hash, user_id, csrf_hash, created_at, expires_at, last_seen_at, auth_method, setup) VALUES (?,?,?,?,?,?,?,?)`,
		idHash, userID, sha256Hex(csrfTokenFor(raw)), now.Format(timeLayout), expires.Format(timeLayout), now.Format(timeLayout), method, setupVal)
	return raw, expires, idHash[:12], err
}

var errNoSession = errors.New("세션이 없거나 만료되었습니다")

// sessionPrincipal 은 쿠키의 원문 세션 ID 로 Principal 과 CSRF 해시를 돌려준다.
func (s *Service) sessionPrincipal(ctx context.Context, raw string) (contract.Principal, string, error) {
	var (
		p                       contract.Principal
		csrfHash, expires, seen string
		revoked, setup          sql.NullString
		method                  string
		role                    string
		active                  int
	)
	err := s.db.QueryRowContext(ctx, `SELECT s.user_id, s.csrf_hash, s.expires_at, s.last_seen_at, s.revoked_at, s.setup, s.auth_method,
			u.name, u.role, u.department_id, u.active
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, sha256Hex(raw)).
		Scan(&p.UserID, &csrfHash, &expires, &seen, &revoked, &setup, &method, &p.Name, &role, &p.DepartmentID, &active)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.Principal{}, "", errNoSession
		}
		return contract.Principal{}, "", err
	}
	now := s.now().UTC()
	lastSeen := parseTime(seen)
	if revoked.Valid || active != 1 || !now.Before(parseTime(expires)) || now.Sub(lastSeen) > sessionIdleTTL {
		return contract.Principal{}, "", errNoSession
	}
	// 퇴사(또는 입사 전)한 사람의 세션은 투영 갱신 전이어도 거부한다.
	if denied, err := org.EmploymentDenied(ctx, s.db, p.UserID, contract.DayOf(s.now())); err != nil {
		return contract.Principal{}, "", err
	} else if denied {
		return contract.Principal{}, "", errNoSession
	}
	if now.Sub(lastSeen) > touchEvery {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now.Format(timeLayout), sha256Hex(raw))
	}
	p.Role = contract.Role(role)
	if err := s.loadPerms(ctx, &p); err != nil {
		return contract.Principal{}, "", err
	}
	p.Setup = setup.String
	p.Channel, p.Auth = contract.ChannelWeb, contract.AuthSession
	return p, csrfHash, nil
}

// loadPerms 는 요청마다 사용자의 실효 권한(B33)을 읽어 Principal 에 채운다. 세션에 권한을 박아 두지 않으므로 회수는 다음 요청부터 적용된다.
// 항상 non-nil 집합을 넣는다(권한이 없으면 빈 집합 = 전부 거부).
func (s *Service) loadPerms(ctx context.Context, p *contract.Principal) error {
	perms, err := access.PermsOf(ctx, s.db, p.UserID)
	if err != nil {
		return err
	}
	p.Perms = perms
	p.RoleNames, err = access.RoleNamesOf(ctx, s.db, p.UserID)
	if err != nil {
		return err
	}
	return s.loadEntities(ctx, p) // 소속·겸직 법인과 권한별 효력 법인(B44)
}

// checkCSRF 는 헤더 토큰의 해시가 세션에 저장된 해시와 같은지 상수 시간으로 비교한다.
func checkCSRF(headerToken, storedHash string) bool {
	if headerToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sha256Hex(headerToken)), []byte(storedHash)) == 1
}

// logout 은 세션을 폐기하고 감사 기록을 남긴다.
func (s *Service) logout(ctx context.Context, p contract.Principal, raw string) error {
	idHash := sha256Hex(raw)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id_hash = ? AND user_id = ? AND revoked_at IS NULL`,
			s.now().UTC().Format(timeLayout), idHash, p.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return s.record(ctx, tx, p, "session.logout", audit.TargetSession, idHash[:12], audit.ResultSuccess, nil, "")
	})
}
