package auth_test

import (
	"context"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/access"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// failingRecorder 는 감사 기록이 항상 실패하는 기록기다.
type failingRecorder struct{}

func (failingRecorder) Record(context.Context, contract.Tx, caudit.Entry) error {
	return errors.New("디스크 가득 참")
}

func newSvc(t *testing.T, rec caudit.Recorder) (*auth.Service, contract.Principal, contract.SQLTxManager) {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil { // 기본 역할 템플릿 권한(B33)
		t.Fatal(err)
	}
	if err := auth.SeedDevUsers(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := auth.SeedDevUsers(ctx, db); err != nil { // 멱등
		t.Fatal(err)
	}
	txs := contract.SQLTxManager{DB: db}
	if rec == nil {
		rec = audit.New(db)
	}
	svc, err := auth.New(auth.Options{DB: db, Txs: txs, Audit: rec})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	_ = db.QueryRow(`SELECT id FROM users WHERE email='admin@pl.test'`).Scan(&id)
	return svc, contract.Principal{UserID: id, Name: "관리자", Role: contract.RoleAdmin, DepartmentID: 1, Channel: contract.ChannelWeb}, txs
}

// 기록이 실패하면 쓰기 요청도 실패하고 아무것도 남지 않는다.
func TestAuditFailureFailsWrites(t *testing.T) {
	svc, admin, txs := newSvc(t, failingRecorder{})
	ctx := context.Background()

	if _, _, err := svc.Issue(ctx, admin, "노트북", 0); err == nil {
		t.Fatal("감사 기록 실패인데 토큰 발급이 성공함")
	}
	var n int
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM personal_tokens`).Scan(&n)
	if n != 0 {
		t.Fatalf("토큰이 남음: %d", n)
	}

	role := contract.RoleManager
	var other int64
	_ = txs.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&other)
	if _, err := svc.Users().Update(ctx, admin, other, contract.UserPatch{Role: &role}); err == nil {
		t.Fatal("감사 기록 실패인데 사용자 수정이 성공함")
	}
	var after contract.Role
	_ = txs.DB.QueryRow(`SELECT role FROM users WHERE id = ?`, other).Scan(&after)
	if after != contract.RoleStaff {
		t.Fatalf("역할이 바뀌었음: %s", after)
	}
}

func TestTokenIssueRequiresPermissionAndBounds(t *testing.T) {
	svc, admin, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, _, err := svc.Issue(ctx, contract.Principal{}, "x", 0); !errors.Is(err, contract.ErrUnauthenticated) && !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("미인증 발급: %v", err)
	}
	for name, ttl := range map[string]time.Duration{"너무 짧음": time.Hour, "너무 김": 400 * 24 * time.Hour} {
		if _, _, err := svc.Issue(ctx, admin, "x", ttl); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := svc.Issue(ctx, admin, "  ", 0); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("빈 이름: %v", err)
	}
	// 사용자 관리는 user:manage 만.
	staff := contract.Principal{UserID: 99, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	if _, err := svc.Users().List(ctx, staff); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("staff 사용자 목록: %v", err)
	}
}

// 개인 토큰으로 인증된 Principal 은 토큰을 발급·폐기할 수 없다.
func TestTokenAuthenticatedPrincipalCannotIssueOrRevoke(t *testing.T) {
	svc, admin, _ := newSvc(t, nil)
	ctx := context.Background()
	viaToken := admin
	viaToken.Auth = contract.AuthToken
	if _, _, err := svc.Issue(ctx, viaToken, "x", 0); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("토큰 인증 Principal 의 발급: %v", err)
	}
	if err := svc.Revoke(ctx, viaToken, 1); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("토큰 인증 Principal 의 폐기: %v", err)
	}
	viaSession := admin
	viaSession.Auth = contract.AuthSession
	if _, _, err := svc.Issue(ctx, viaSession, "x", 0); err != nil {
		t.Errorf("세션 Principal 의 발급: %v", err)
	}
}
