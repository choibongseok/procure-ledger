package llmgw_test

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 회로 차단 임계: 연속 5회 실패, 또는 최근 20회 중 10회 이상이고 실패가 절반 이상. 성공이 섞이면 연속 실패가 끊긴다.
func TestBreakerThresholds(t *testing.T) {
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	b := llmgw.NewBreakerSet(func() time.Time { return now })
	var events []string
	b.OnChange = func(code, ev string) { events = append(events, code+":"+ev) }
	// 성공이 섞이면 연속 5회에 못 미친다(실패 비율은 10회 미만이라 아직 판정하지 않는다).
	for i := 0; i < 4; i++ {
		b.Failure("a")
	}
	b.Success("a")
	b.Failure("a")
	if st, _ := b.State("a"); st != llmgw.CircuitClosed {
		t.Fatalf("아직 닫혀 있어야 한다: %s", st)
	}
	// 10회 이상, 실패가 절반 이상이면 연속이 아니어도 열린다.
	c := llmgw.NewBreakerSet(func() time.Time { return now })
	for i := 0; i < 11; i++ {
		if i%2 == 0 {
			c.Failure("b")
		} else {
			c.Success("b")
		}
	}
	if st, _ := c.State("b"); st != llmgw.CircuitOpen {
		t.Fatalf("11회 중 6회 실패(절반 이상)면 열려야 한다: %s", st)
	}
	// 재시도 불가 오류는 회로에 영향이 없다: Release 는 반열림 표시만 푼다.
	d := llmgw.NewBreakerSet(func() time.Time { return now })
	for i := 0; i < 5; i++ {
		d.Failure("c")
	}
	if d.Allow("c") {
		t.Fatal("열려 있으면 허용하지 않는다")
	}
	now = now.Add(61 * time.Second)
	if !d.Allow("c") || d.Allow("c") {
		t.Fatal("반열림은 시험 한 번만 허용한다")
	}
	d.Release("c")
	if !d.Allow("c") {
		t.Fatal("시험이 결과 없이 끝나면 다시 시험할 수 있다")
	}
	d.Success("c")
	if st, _ := d.State("c"); st != llmgw.CircuitClosed {
		t.Fatalf("반열림 성공은 닫는다: %s", st)
	}
	_ = events
}

// 관리 서비스는 공급자 상태 점(정상·회로 차단·설정 확인 필요·꺼짐)과 24시간 통계를 준다.
func TestProviderListShowsHealth(t *testing.T) {
	e := newEnv(t)
	br := llmgw.NewBreakerSet(func() time.Time { return e.now })
	ad := e.admin(t, false)
	ad.Breakers = br
	ctx := adminCtx(contract.PermLLMProviderManage)
	mk := func(code string, on bool) {
		pv, err := ad.SaveProvider(ctx, llmgw.ProviderInput{Code: code, Kind: "mock", Name: code})
		if err != nil {
			t.Fatal(err)
		}
		if on {
			if _, err := ad.SetActive(ctx, pv.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("h-ok", true)
	mk("h-open", true)
	mk("h-auth", true)
	mk("h-off", false)
	for i := 0; i < 5; i++ {
		br.Failure("h-open")
	}
	br.AuthFailed("h-auth")
	list, err := ad.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range list.Providers {
		got[p.Code] = p.Health
	}
	want := map[string]string{"h-ok": "ok", "h-open": "circuit_open", "h-auth": "check_settings", "h-off": "off"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: 기대 %s, 실제 %s", k, v, got[k])
		}
	}
}
