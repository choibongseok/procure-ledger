package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// MailEnabled 는 메일 발송이 설정됐는지다(로그인 화면의 "비밀번호를 잊으셨나요" 노출 기준).
func (s *Service) MailEnabled() bool { return s.mailer != nil }

// 링크 문구. 이름·권한 정보는 넣지 않는다.
func (s *Service) linkMail(kind, token string) (subject, body string) {
	link := s.base + "/login/reset?token=" + url.QueryEscape(token)
	if kind == "setup" {
		return "[ProcureLedger] 비밀번호 설정 링크", "ProcureLedger 로그인용 비밀번호를 정하려면 아래 링크를 여세요. 링크는 72시간 동안, 한 번만 쓸 수 있습니다.\n\n" + link + "\n"
	}
	return "[ProcureLedger] 비밀번호 재설정 링크", "비밀번호를 다시 정하려면 아래 링크를 여세요. 링크는 30분 동안, 한 번만 쓸 수 있습니다.\n\n" + link + "\n\n요청하지 않았다면 이 메일을 무시하세요.\n"
}

// sendLinkMail 은 메일을 보내고 실패하면 감사에 error 로 남긴다(요청 응답에는 드러내지 않는다: 계정 열거 방지).
// 기본은 별도 고루틴(응답 시간이 계정 유무에 좌우되지 않게)이고, 시험은 SyncMail 로 동기 실행한다.
func (s *Service) sendLinkMail(userID int64, to, kind, token, action string) {
	subject, body := s.linkMail(kind, token)
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := s.mailer.Send(ctx, to, subject, body); err != nil {
			log.Printf("auth: 메일 발송 실패(사용자 %d): %v", userID, err)
			_ = s.txs.WithTx(ctx, func(tx contract.Tx) error {
				return s.record(ctx, tx, contract.Principal{Channel: contract.ChannelWeb}, action, audit.TargetCredential, idStr(userID), audit.ResultError,
					[]audit.DiffItem{{Key: "via", New: "mail"}}, "메일 발송 실패")
			})
		}
	}
	if s.syncMail {
		run()
		return
	}
	go run()
}

// issueLinkToken 은 새 링크 토큰을 만들고 이전 토큰을 무효로 한다. 원문은 돌려주고 DB 에는 해시만 둔다.
func (s *Service) issueLinkToken(ctx context.Context, tx contract.Tx, userID int64, kind string, ttl time.Duration) (string, error) {
	raw, err := randomString(32)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, now.Format(timeLayout), userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE expires_at < ?`, now.Add(-24*time.Hour).Format(timeLayout)); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO password_reset_tokens (token_hash, user_id, kind, created_at, expires_at) VALUES (?,?,?,?,?)`,
		sha256Hex(raw), userID, kind, now.Format(timeLayout), now.Add(ttl).Format(timeLayout))
	return raw, err
}

// RequestReset 은 "비밀번호를 잊으셨나요"(명세 6.1)다. 항상 같은 결과를 돌려주고(오류는 서버 내부 문제뿐),
// 비밀번호가 있는 재직·활성 계정에만 링크를 보낸다. 사용자당 1시간 3건까지.
func (s *Service) RequestReset(ctx context.Context, email string) error {
	if s.mailer == nil {
		return fmt.Errorf("%w: 메일 발송이 설정되지 않았습니다. 관리자에게 임시 비밀번호를 요청하세요", contract.ErrInvalidState)
	}
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return err
	}
	email = normEmail(email)
	var userID int64
	var addr string
	err = s.db.QueryRowContext(ctx, `SELECT u.id, u.email FROM users u JOIN user_credentials c ON c.user_id = u.id WHERE lower(u.email) = ?`, email).Scan(&userID, &addr)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !pol.EmailLoginEnabled && !s.isEmergencyAdmin(ctx, pol, userID)) {
		return nil // 같은 응답
	}
	if err != nil {
		return err
	}
	var token string
	sent := false
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		u, err := s.resolveUserID(ctx, tx, userID)
		if err != nil {
			if errors.Is(err, contract.ErrForbidden) {
				return nil // 퇴사·비활성은 링크를 보내지 않는다(응답은 같다)
			}
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_reset_tokens WHERE user_id = ? AND created_at > ?`,
			userID, s.now().UTC().Add(-time.Hour).Format(timeLayout)).Scan(&n); err != nil {
			return err
		}
		if n >= resetPerHour {
			return nil
		}
		if token, err = s.issueLinkToken(ctx, tx, userID, "reset", resetLinkTTL); err != nil {
			return err
		}
		sent = true
		return s.record(ctx, tx, u, "auth.password_reset_request", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "via", New: "mail"}}, "")
	})
	if err != nil {
		return err
	}
	if sent {
		s.sendLinkMail(userID, addr, "reset", token, "auth.password_reset_request")
	}
	return nil
}

// ErrBadLink 는 링크가 없거나 만료됐거나 이미 쓰인 경우다.
var ErrBadLink = fmt.Errorf("%w: 링크가 만료되었거나 이미 사용되었습니다. 다시 요청해 주세요", contract.ErrValidation)

// ResetPassword 는 링크로 새 비밀번호를 정한다(재설정·최초 설정). 성공하면 사용자의 모든 세션을 폐기하고 잠금을 푼다.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if token == "" {
		return ErrBadLink
	}
	pol, err := loadPolicy(ctx, s.db)
	if err != nil {
		return err
	}
	var userID int64
	var kind, exp string
	var used sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT user_id, kind, expires_at, used_at FROM password_reset_tokens WHERE token_hash = ?`, sha256Hex(token)).Scan(&userID, &kind, &exp, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBadLink
	}
	if err != nil {
		return err
	}
	if !pol.EmailLoginEnabled && !s.isEmergencyAdmin(ctx, pol, userID) {
		return ErrEmailLoginOff // E12: 이메일 로그인이 꺼져 있으면 비상 관리자만 재설정할 수 있다
	}
	if used.Valid || !s.now().UTC().Before(parseTime(exp)) {
		return ErrBadLink
	}
	email, name, err := s.userEmailName(ctx, userID)
	if err != nil {
		return err
	}
	if err := pol.Password.Check(password, email, name); err != nil {
		return err // 정책 위반이면 링크를 소비하지 않고 다시 입력하게 한다
	}
	oldHash := ""
	if c, err := scanCred(s.db.QueryRowContext(ctx, credSelect+` WHERE c.user_id = ?`, userID)); err == nil {
		oldHash = c.hash
		if err := s.checkReuse(ctx, s.db, userID, c.hash, password, pol.Password.HistoryCount); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	newHash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	bad := false
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
			s.now().UTC().Format(timeLayout), sha256Hex(token), s.now().UTC().Format(timeLayout))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 { // 동시에 다른 요청이 먼저 썼다
			bad = true
			return nil
		}
		u, err := s.resolveUserID(ctx, tx, userID)
		if err != nil {
			if errors.Is(err, contract.ErrForbidden) {
				bad = true // 퇴사·비활성: 링크도 무효(같은 문구)
				return nil
			}
			return err
		}
		if err := s.setPassword(ctx, tx, userID, newHash, oldHash, pol.Password, false); err != nil {
			return err
		}
		if err := s.revokeUserSessions(ctx, tx, userID, ""); err != nil {
			return err
		}
		return s.record(ctx, tx, u, "auth.password_reset", audit.TargetCredential, idStr(userID), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "via", New: map[string]string{"reset": "mail", "setup": "mail_setup"}[kind]}}, "")
	})
	if err != nil {
		return err
	}
	if bad {
		return ErrBadLink
	}
	return nil
}
