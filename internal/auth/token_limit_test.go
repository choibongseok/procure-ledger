package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 사용자당 유효 토큰 개수 상한: 상한에서 발급이 거부되고, 폐기·만료된 토큰은 세지 않으며, 다른 사용자와 합산하지 않는다.
func TestTokenActiveLimit(t *testing.T) {
	_, admin, txs := newSvc(t, nil)
	clock := time.Now()
	svc, err := auth.New(auth.Options{DB: txs.DB, Txs: txs, Audit: audit.New(txs.DB), MaxActiveTokens: 2,
		Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, first, err := svc.Issue(ctx, admin, "a", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Issue(ctx, admin, "b", 0); err != nil {
		t.Fatal(err)
	}

	_, _, err = svc.Issue(ctx, admin, "c", 0)
	if !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("상한 초과 발급이 거부되지 않음: %v", err)
	}
	for _, want := range []string{"상한(2개)", "폐기한 뒤"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("문구 %q 없음: %v", want, err)
		}
	}
	var n int
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM personal_tokens WHERE user_id = ?`, admin.UserID).Scan(&n)
	if n != 2 {
		t.Fatalf("거부된 발급이 토큰을 남김: %d", n)
	}
	var issued int
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'token.issue'`).Scan(&issued)
	if issued != 2 {
		t.Fatalf("거부된 발급이 감사 행을 남김: %d", issued)
	}

	// 다른 사용자는 별개로 센다.
	var other int64
	_ = txs.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&other)
	staff := contract.Principal{UserID: other, Name: "직원", Role: contract.RoleStaff, DepartmentID: 1, Channel: contract.ChannelWeb}
	if _, _, err := svc.Issue(ctx, staff, "s", 0); err != nil {
		t.Fatalf("다른 사용자 발급: %v", err)
	}

	// 폐기하면 다시 발급된다.
	if err := svc.Revoke(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Issue(ctx, admin, "d", 0); err != nil {
		t.Fatalf("폐기 뒤 발급: %v", err)
	}
	if _, _, err := svc.Issue(ctx, admin, "e", 0); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("다시 상한: %v", err)
	}

	// 만료된 토큰은 세지 않는다(기본 30일 토큰 둘이 만료된 뒤).
	clock = clock.Add(31 * 24 * time.Hour)
	if _, _, err := svc.Issue(ctx, admin, "f", 0); err != nil {
		t.Fatalf("만료 뒤 발급: %v", err)
	}
}

// 상한 0 은 제한 없음이다.
func TestTokenActiveLimitZeroIsUnlimited(t *testing.T) {
	svc, admin, _ := newSvc(t, nil)
	for i := 0; i < 15; i++ {
		if _, _, err := svc.Issue(context.Background(), admin, "t", 0); err != nil {
			t.Fatalf("%d번째: %v", i, err)
		}
	}
}
