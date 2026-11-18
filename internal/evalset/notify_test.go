package evalset_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/evalset"
)

// 기준선보다 허용 하락을 넘게 낮은 실행은 앱 안 알림을 남긴다(평가 담당 = eval:write 보유자). 하락이 없으면 알림이 없고, 저장 전 평가(gate)는 알리지 않는다.
func TestDropNotification(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Exec(`INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id) VALUES (1, 'eval:write', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	e.published("faq", [3]string{"가", "가", "contains"}, [3]string{"나", "나", "contains"})
	good := true
	svc := e.runSvc(&fakeLLM{reply: func(req llm.Request, _ int) (string, error) {
		if good {
			return req.Messages[len(req.Messages)-1].Content, nil
		}
		return "엉뚱한 답", nil
	}}, false)
	run := func() evalset.RunView {
		v, err := svc.Start(admin, "faq", evalset.RunInput{ConfirmCost: true})
		if err != nil {
			t.Fatal(err)
		}
		return waitDone(t, svc, v.ID)
	}
	count := func() int {
		var n int
		_ = e.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'evalset_drop'`).Scan(&n)
		return n
	}
	a := run()
	if _, err := svc.SetBaseline(admin, "faq", a.ID); err != nil {
		t.Fatal(err)
	}
	run()
	if count() != 0 {
		t.Fatalf("하락이 없는데 알림 %d건", count())
	}
	good = false
	b := run()
	if count() != 1 {
		t.Fatalf("하락 알림 %d건", count())
	}
	var title, body, link string
	var uid int64
	if err := e.db.QueryRow(`SELECT user_id, title, body, link FROM notifications WHERE kind = 'evalset_drop'`).Scan(&uid, &title, &body, &link); err != nil {
		t.Fatal(err)
	}
	if uid != 1 || !strings.Contains(title, "시험 세트") || !strings.Contains(body, "기준선 100% 보다 100%p 낮습니다") || link != fmt.Sprintf("/admin/eval/runs/%d", b.ID) {
		t.Fatalf("알림: %d %q %q %q", uid, title, body, link)
	}
	// 저장 전 평가 실행(동기)은 알리지 않는다.
	bad := &fakeLLM{reply: func(llm.Request, int) (string, error) { return "엉뚱", nil }}
	if _, err := svc.RunSync(context.Background(), contract.Principal{}, "faq", evalset.RunInput{Client: bad, Label: "x", Mode: evalset.ModeGate}); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("게이트 실행이 알림을 만들었습니다: %d", count())
	}
}

// 저장 전 평가 모드는 공급자 오류를 점수로 세지 않고 실행을 실패(건너뜀 사유)로 끝낸다.
func TestGateModeProviderErrorFailsRun(t *testing.T) {
	e := newEnv(t)
	e.published("faq", [3]string{"가", "가", "contains"}, [3]string{"나", "나", "contains"})
	svc := e.runSvc(nil, false)
	boom := &fakeLLM{reply: func(llm.Request, int) (string, error) { return "", errors.New("boom") }}
	v, err := svc.RunSync(context.Background(), contract.Principal{}, "faq", evalset.RunInput{Client: boom, Label: "x", Mode: evalset.ModeGate})
	if err != nil || v.Status != "failed" || !strings.Contains(v.FailReason, "오류") {
		t.Fatalf("%+v %v", v, err)
	}
}
