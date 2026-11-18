package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const (
	tokenPrefix     = "plp_"
	DefaultTokenTTL = 30 * 24 * time.Hour
	MaxTokenTTL     = 365 * 24 * time.Hour
	MinTokenTTL     = 24 * time.Hour
)

// authorizeTokenWrite 는 토큰 발급·폐기의 서비스 계층 방어선이다. token:manage 권한에 더해
// 개인 토큰으로 들어온 요청은 거부한다(유출된 단기 토큰이 장기 토큰을 찍어 내는 경로 차단).
// HTTP 라우트는 rbac.SessionOnly 로 먼저 막고, 거부는 감사 로그에 남는다.
func (s *Service) authorizeTokenWrite(ctx context.Context, p contract.Principal) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermTokenManage); err != nil {
		return err
	}
	if p.Auth == contract.AuthToken {
		return fmt.Errorf("%w: 개인 토큰으로는 토큰을 발급하거나 폐기할 수 없습니다(로그인 세션 필요)", contract.ErrForbidden)
	}
	return nil
}

// tokenLimitError 는 유효 토큰 개수 상한 초과다. 메시지를 그대로 화면에 보이고, errors.Is(ErrInvalidState) 이므로 HTTP 409 로 옮겨진다.
type tokenLimitError struct{ max int }

func (e tokenLimitError) Error() string {
	return fmt.Sprintf("사용 중인 개인 토큰이 상한(%d개)에 도달했습니다. 쓰지 않는 토큰을 폐기한 뒤 다시 발급하세요.", e.max)
}

func (tokenLimitError) Is(target error) bool { return target == contract.ErrInvalidState }

// Issue 는 본인 개인 토큰을 발급한다. 원문(raw)은 이 반환값으로만 나가고 저장되지 않는다.
// ttl 이 0 이면 30일, 범위는 1일~365일(만료 필수).
func (s *Service) Issue(ctx context.Context, p contract.Principal, name string, ttl time.Duration) (string, contract.TokenInfo, error) {
	if err := s.authorizeTokenWrite(ctx, p); err != nil {
		return "", contract.TokenInfo{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return "", contract.TokenInfo{}, fmt.Errorf("%w: 토큰 이름은 1~80자여야 합니다", contract.ErrValidation)
	}
	if ttl == 0 {
		ttl = DefaultTokenTTL
	}
	if ttl < MinTokenTTL || ttl > MaxTokenTTL {
		return "", contract.TokenInfo{}, fmt.Errorf("%w: 유효 기간은 1일~365일이어야 합니다", contract.ErrValidation)
	}
	body, err := randomString(32)
	if err != nil {
		return "", contract.TokenInfo{}, err
	}
	raw := tokenPrefix + body
	now := s.now().UTC()
	info := contract.TokenInfo{Name: name, Prefix: raw[:len(tokenPrefix)+4], CreatedAt: now, ExpiresAt: now.Add(ttl)}
	actor := s.webActor(p)
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if s.maxActiveTokens > 0 { // 개수 확인과 삽입이 같은 트랜잭션이라 동시 발급으로 상한을 넘길 수 없다
			// 만료 판정은 AuthenticateToken 과 같게 시각을 파싱해 비교한다(문자열 비교는 소수 초에서 어긋난다).
			rows, err := tx.QueryContext(ctx, `SELECT expires_at FROM personal_tokens WHERE user_id = ? AND revoked_at IS NULL`, p.UserID)
			if err != nil {
				return err
			}
			active := 0
			for rows.Next() {
				var exp string
				if err := rows.Scan(&exp); err != nil {
					rows.Close()
					return err
				}
				if now.Before(parseTime(exp)) {
					active++
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if active >= s.maxActiveTokens {
				return tokenLimitError{max: s.maxActiveTokens}
			}
		}
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO personal_tokens (user_id, name, token_prefix, token_hash, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
			p.UserID, info.Name, info.Prefix, sha256Hex(raw), now.Format(timeLayout), info.ExpiresAt.Format(timeLayout))
		if err != nil {
			return err
		}
		info.ID = id
		return s.record(ctx, tx, actor, "token.issue", audit.TargetToken, idStr(info.ID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "name", New: info.Name}, {Key: "expires_at", New: info.ExpiresAt.Format(time.RFC3339)}}, "")
	})
	if err != nil {
		return "", contract.TokenInfo{}, err
	}
	return raw, info, nil
}

// List 는 본인 토큰 목록이다(폐기된 것 포함, 최신순).
func (s *Service) List(ctx context.Context, p contract.Principal) ([]contract.TokenInfo, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermTokenManage); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_prefix, created_at, expires_at, revoked_at, last_used_at
		FROM personal_tokens WHERE user_id = ? ORDER BY id DESC`, p.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.TokenInfo{}
	for rows.Next() {
		var t contract.TokenInfo
		var created, expires string
		var revoked, used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &expires, &revoked, &used); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt = parseTime(created), parseTime(expires)
		t.RevokedAt, t.LastUsedAt = parseTimePtr(revoked), parseTimePtr(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke 는 본인 토큰을 폐기한다. 남의 토큰이거나 없으면 ErrNotFound, 이미 폐기됐으면 그대로 성공(멱등).
func (s *Service) Revoke(ctx context.Context, p contract.Principal, id int64) error {
	if err := s.authorizeTokenWrite(ctx, p); err != nil {
		return err
	}
	actor := s.webActor(p)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM personal_tokens WHERE id = ? AND user_id = ?`, id, p.UserID).Scan(&revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if revoked.Valid {
			return nil
		}
		now := s.now().UTC().Format(timeLayout)
		if _, err := tx.ExecContext(ctx, `UPDATE personal_tokens SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, "token.revoke", audit.TargetToken, idStr(id), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "revoked_at", New: now}}, "")
	})
}

// AuthenticateToken 은 contract.Authenticator 를 구현한다. 해시 비교, 만료·폐기·비활성 사용자는 ErrUnauthenticated.
// 반환 Principal 의 Channel 은 mcp 다(HTTP 미들웨어는 web 으로 덮어쓴다).
func (s *Service) AuthenticateToken(ctx context.Context, raw string) (contract.Principal, error) {
	if !strings.HasPrefix(raw, tokenPrefix) {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	var (
		p             contract.Principal
		role, expires string
		revoked       sql.NullString
		active        int
		tokenID       int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT t.id, t.user_id, t.expires_at, t.revoked_at,
			u.name, u.role, u.department_id, u.active
		FROM personal_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, sha256Hex(raw)).
		Scan(&tokenID, &p.UserID, &expires, &revoked, &p.Name, &role, &p.DepartmentID, &active)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.Principal{}, contract.ErrUnauthenticated
		}
		return contract.Principal{}, err
	}
	now := s.now().UTC()
	if revoked.Valid || active != 1 || !now.Before(parseTime(expires)) {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	// 퇴사(또는 입사 전)한 사람은 users.active 가 아직 1 이어도(투영 갱신 전) 거부한다. 서버와 erp-mcp 가 같은 판정을 쓴다.
	if denied, err := org.EmploymentDenied(ctx, s.db, p.UserID, contract.DayOf(s.now())); err != nil {
		return contract.Principal{}, err
	} else if denied {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE personal_tokens SET last_used_at = ? WHERE id = ?`, now.Format(timeLayout), tokenID)
	p.Role = contract.Role(role)
	if err := s.loadPerms(ctx, &p); err != nil { // 토큰은 발급자의 현재 실효 권한을 따른다(요청마다 계산)
		return contract.Principal{}, err
	}
	p.Channel, p.Auth = contract.ChannelMCP, contract.AuthToken
	p.TokenID = tokenID
	return p, nil
}

// TokenInfos 는 토큰 번호를 이름·앞자리·소유자로 바꾼다(contract.TokenLookup). 원문과 해시는 읽지도 돌려주지도 않는다.
// 폐기·만료된 토큰도 조회된다. 없는 번호는 결과 맵에 빠진다. 권한 검사는 호출하는 쪽(usage:read 라우트)이 한다.
func (s *Service) TokenInfos(ctx context.Context, ids []int64) (map[int64]contract.TokenInfo, error) {
	out := map[int64]contract.TokenInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.name, t.token_prefix, t.created_at, t.expires_at, t.revoked_at, t.last_used_at, u.name
		FROM personal_tokens t JOIN users u ON u.id = t.user_id WHERE t.id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t contract.TokenInfo
		var created, expires string
		var revoked, used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &expires, &revoked, &used, &t.OwnerName); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt = parseTime(created), parseTime(expires)
		t.RevokedAt, t.LastUsedAt = parseTimePtr(revoked), parseTimePtr(used)
		out[t.ID] = t
	}
	return out, rows.Err()
}
