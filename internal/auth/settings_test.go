package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func TestLLMImageSettingDefaultOffAdminOnlyAndAudited(t *testing.T) {
	svc, admin, txs := newSvc(t, nil)
	ctx := context.Background()
	st := svc.Settings()
	if st.LLMImageTransmission(ctx) {
		t.Fatal("이미지 전송은 기본 꺼짐이어야 한다")
	}
	for _, role := range []contract.Role{contract.RoleFinance, contract.RoleManager, contract.RoleStaff, contract.RoleAuditor} {
		p := contract.Principal{UserID: 99, Role: role, Channel: contract.ChannelWeb}
		if err := st.SetLLMImageTransmission(ctx, p, true); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s 의 설정 변경: %v", role, err)
		}
	}
	if st.LLMImageTransmission(ctx) {
		t.Fatal("권한 없는 변경이 반영됨")
	}
	if err := st.SetLLMImageTransmission(ctx, admin, true); err != nil {
		t.Fatal(err)
	}
	if !st.LLMImageTransmission(ctx) {
		t.Fatal("admin 변경이 반영되지 않음")
	}
	// 같은 값으로 다시 바꾸면 기록이 늘지 않는다.
	if err := st.SetLLMImageTransmission(ctx, admin, true); err != nil {
		t.Fatal(err)
	}
	var n int
	var diff string
	_ = txs.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='setting.update' AND target_type=?`, string(caudit.TargetSetting)).Scan(&n)
	_ = txs.DB.QueryRow(`SELECT diff FROM audit_logs WHERE action='setting.update'`).Scan(&diff)
	if n != 1 || diff == "" {
		t.Fatalf("setting.update 감사 %d건, diff %q", n, diff)
	}
}

func TestLLMImageSettingAuditFailureRollsBack(t *testing.T) {
	svc, admin, _ := newSvc(t, failingRecorder{})
	ctx := context.Background()
	if err := svc.Settings().SetLLMImageTransmission(ctx, admin, true); err == nil {
		t.Fatal("감사 기록 실패인데 설정 변경이 성공함")
	}
	if svc.Settings().LLMImageTransmission(ctx) {
		t.Fatal("감사 실패 뒤에도 설정이 켜짐")
	}
}
