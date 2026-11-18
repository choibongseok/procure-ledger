package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenRow 는 인증을 통과한 연동 토큰이다(해시·원문은 담지 않는다).
type tokenRow struct {
	ID        int64
	Name      string
	Source    string
	Scope     string
	CreatedBy int64
}

// TokenInfo 는 화면에 보이는 연동 토큰 한 건이다(원문 없음).
type TokenInfo struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Source       string     `json:"source"`
	Scope        string     `json:"scope"`
	Prefix       string     `json:"prefix"`
	CreatedBy    int64      `json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
	IssuerActive bool       `json:"issuerActive"` // 발급자가 지금도 재직(계정 사용 가능)인가. 퇴사했으면 화면이 경고한다(명세 6.4)
}

func inList(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// authorizeWrite 는 토큰 발급·폐기·설정 변경의 서비스 계층 방어선이다. scim:manage 에 더해 Bearer(개인·연동 토큰)로 들어온 요청은 거부한다.
func (s *Service) authorizeWrite(ctx context.Context, p contract.Principal) error {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermSCIMManage); err != nil {
		return err
	}
	if p.Auth == contract.AuthToken || p.Auth == contract.AuthSCIM {
		return fmt.Errorf("%w: 토큰으로는 연동 토큰을 만들거나 바꿀 수 없습니다(로그인 세션 필요)", contract.ErrForbidden)
	}
	return nil
}

func (s *Service) webActor(p contract.Principal) contract.Principal {
	p.Channel = contract.ChannelWeb
	return p
}

// Issue 는 연동 토큰을 발급한다. 원문(raw)은 이 반환값으로만 나가고 저장되지 않는다.
// ttl 이 0 이면 180일, 범위는 1일~365일. 같은 출처의 유효 토큰은 상한(기본 2)까지다.
func (s *Service) Issue(ctx context.Context, p contract.Principal, name, source, scope string, ttl time.Duration) (string, TokenInfo, error) {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return "", TokenInfo{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return "", TokenInfo{}, fmt.Errorf("%w: 토큰 이름은 1~80자여야 합니다", contract.ErrValidation)
	}
	if !inList(Sources, source) {
		return "", TokenInfo{}, fmt.Errorf("%w: 출처를 고르세요(entra·okta·google·other)", contract.ErrValidation)
	}
	if scope == "" {
		scope = "users"
	}
	if !inList(Scopes, scope) {
		return "", TokenInfo{}, fmt.Errorf("%w: 범위를 고르세요(users·users_groups)", contract.ErrValidation)
	}
	if ttl == 0 {
		ttl = DefaultTokenTTL
	}
	if ttl < MinTokenTTL || ttl > MaxTokenTTL {
		return "", TokenInfo{}, fmt.Errorf("%w: 유효 기간은 1일~365일이어야 합니다", contract.ErrValidation)
	}
	body, err := randomString(32)
	if err != nil {
		return "", TokenInfo{}, err
	}
	raw := tokenPrefix + body
	now := s.now().UTC()
	info := TokenInfo{Name: name, Source: source, Scope: scope, Prefix: raw[:len(tokenPrefix)+4], CreatedBy: p.UserID, CreatedAt: now, ExpiresAt: now.Add(ttl), IssuerActive: true}
	actor := s.webActor(p)
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if s.maxToken > 0 { // 개수 확인과 삽입이 같은 트랜잭션이라 동시 발급으로 상한을 넘길 수 없다
			rows, err := tx.QueryContext(ctx, `SELECT expires_at FROM scim_tokens WHERE source = ? AND revoked_at IS NULL`, source)
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
			if active >= s.maxToken {
				return limitError{max: s.maxToken}
			}
		}
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO scim_tokens (name, source, scope, token_prefix, token_hash, created_by, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?)`,
			info.Name, source, scope, info.Prefix, sha256Hex(raw), p.UserID, now.Format(timeLayout), info.ExpiresAt.Format(timeLayout))
		if err != nil {
			return err
		}
		info.ID = id
		return s.record(ctx, tx, actor, "scim.token_issue", audit.TargetToken, strconv.FormatInt(id, 10), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "name", New: info.Name}, {Key: "source", New: source}, {Key: "scope", New: scope}, {Key: "expires_at", New: info.ExpiresAt.Format(time.RFC3339)}}, "SCIM 연동 토큰")
	})
	if err != nil {
		return "", TokenInfo{}, err
	}
	return raw, info, nil
}

type limitError struct{ max int }

func (e limitError) Error() string {
	return fmt.Sprintf("같은 출처의 유효한 연동 토큰이 상한(%d개)에 도달했습니다. 교체 중이 아니라면 쓰지 않는 토큰을 폐기한 뒤 다시 발급하세요.", e.max)
}
func (limitError) Is(target error) bool { return target == contract.ErrInvalidState }

// List 는 연동 토큰 목록이다(폐기된 것 포함, 최신순).
func (s *Service) List(ctx context.Context, p contract.Principal) ([]TokenInfo, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermSCIMRead); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.name, t.source, t.scope, t.token_prefix, t.created_by, t.created_at, t.expires_at, t.revoked_at, t.last_used_at, u.active
		FROM scim_tokens t JOIN users u ON u.id = t.created_by ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenInfo{}
	for rows.Next() {
		var t TokenInfo
		var created, expires string
		var revoked, used sql.NullString
		var active int
		if err := rows.Scan(&t.ID, &t.Name, &t.Source, &t.Scope, &t.Prefix, &t.CreatedBy, &created, &expires, &revoked, &used, &active); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt = parseTime(created), parseTime(expires)
		t.RevokedAt, t.LastUsedAt = parseTimePtr(revoked), parseTimePtr(used)
		t.IssuerActive = active == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke 는 연동 토큰을 즉시 폐기한다. 이미 처리 중인 요청은 끝까지 간다.
func (s *Service) Revoke(ctx context.Context, p contract.Principal, id int64, reason string) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 200 {
		return fmt.Errorf("%w: 사유는 200자 이내로 적어 주세요", contract.ErrValidation)
	}
	actor := s.webActor(p)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var name string
		var revoked sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT name, revoked_at FROM scim_tokens WHERE id = ?`, id).Scan(&name, &revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return contract.ErrNotFound
			}
			return err
		}
		if revoked.Valid {
			return stateError("이미 폐기된 토큰입니다")
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, `UPDATE scim_tokens SET revoked_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, "scim.token_revoke", audit.TargetToken, strconv.FormatInt(id, 10), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "name", New: name}, {Key: "reason", New: reason}}, "SCIM 연동 토큰")
	})
}

// authenticate 는 원문 토큰을 확인한다. 없거나 폐기·만료면 ok=false(원인을 구분해 돌려주지 않는다).
func (s *Service) authenticate(ctx context.Context, raw string) (tokenRow, bool, error) {
	if !strings.HasPrefix(raw, tokenPrefix) || len(raw) > 200 {
		return tokenRow{}, false, nil
	}
	var t tokenRow
	var expires string
	var revoked, used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, source, scope, created_by, expires_at, revoked_at, last_used_at FROM scim_tokens WHERE token_hash = ?`, sha256Hex(raw)).
		Scan(&t.ID, &t.Name, &t.Source, &t.Scope, &t.CreatedBy, &expires, &revoked, &used)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tokenRow{}, false, nil
		}
		return tokenRow{}, false, err
	}
	now := s.now().UTC()
	if revoked.Valid || !now.Before(parseTime(expires)) {
		return tokenRow{}, false, nil
	}
	// 마지막 사용 시각은 1분에 한 번만 쓴다(요청마다 쓰기를 늘리지 않는다).
	if u := parseTimePtr(used); u == nil || now.Sub(*u) >= time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE scim_tokens SET last_used_at = ? WHERE id = ?`, now.Format(timeLayout), t.ID)
	}
	return t, true, nil
}

// stateError 는 화면에 그대로 보일 업무 문구를 가진 상태 충돌(409)이다.
type stateError string

func (e stateError) Error() string      { return string(e) }
func (stateError) Is(target error) bool { return target == contract.ErrInvalidState }
