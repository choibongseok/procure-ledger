package main

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 비활성 사용자의 실효 권한 캐시는 비워지고, 다시 켜면 부여가 다시 계산된다.
func TestDeactivatedUserHasNoEffectivePermissions(t *testing.T) {
	e := newPlatformEnvWith(t, true, cheapArgon)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	ah := jsonHdr(map[string]string{"X-CSRF-Token": e.csrf(admin)})
	var staffID int64
	_ = e.d.DB.QueryRow(`SELECT id FROM users WHERE email='staff@pl.test'`).Scan(&staffID)
	count := func() int {
		var n int
		_ = e.d.DB.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions WHERE user_id=?`, staffID).Scan(&n)
		return n
	}
	if count() == 0 {
		t.Fatal("시험 전제: 직원의 실효 권한이 있어야 함")
	}
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"active":false}`, ah); code != 200 {
		t.Fatalf("비활성: %d %s", code, b)
	}
	if n := count(); n != 0 {
		t.Fatalf("비활성 뒤 캐시 %d행", n)
	}
	if _, err := access.RecomputeAll(t.Context(), e.d.DB, contract.DayOf(time.Now())); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("재계산 뒤에도 비활성 사용자 캐시 %d행", n)
	}
	if code, b := e.call(admin, "PATCH", "/api/admin/users/"+itoa(staffID), `{"active":true}`, ah); code != 200 {
		t.Fatalf("재활성: %d %s", code, b)
	}
	if count() == 0 {
		t.Fatal("재활성 뒤 캐시가 다시 계산되지 않음")
	}
}
