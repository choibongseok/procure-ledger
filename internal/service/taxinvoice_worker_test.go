package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/taxasp"
	"github.com/choibongseok/procure-ledger/internal/taxasp/mock"
)

// B43 1단계(ai): 전송 작업이 실제 서비스(전이 표·전송 기록·감사) 위에서 모의 대행사와 함께 도는지 본다.

func (e *env) taxWorker(mode string, on bool) (*taxasp.Worker, *taxasp.Registry) {
	reg := taxasp.NewRegistry()
	reg.Register("mock", mock.Factory(mock.NewStore()))
	src := taxasp.Static{Cfg: taxasp.Config{Provider: "mock", MockMode: mode}, On: on}
	e.tax.ASPReady = taxasp.Ready(src, reg)
	return &taxasp.Worker{Store: e.tax, Settings: src, Registry: reg, Now: func() time.Time { return e.now }}, reg
}

func (e *env) taxIssued(t *testing.T, supply int64) domain.TaxInvoice {
	t.Helper()
	v := e.taxDraft(t, supply)
	got, err := e.tax.Issue(as(uFinance2), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (e *env) taxStatus(t *testing.T, id int64) domain.TaxInvoice {
	t.Helper()
	v, err := e.tax.Get(as(uFinance), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 순서대로 두 번 돌려 sent → accepted 까지 간다(none 모드).
func TestTaxWorkerNormalFlow(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("none", true)
	v := e.taxIssued(t, 100_000)
	e.now = e.now.Add(time.Second)
	if n, err := w.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("첫 주기: %d %v", n, err)
	}
	if got := e.taxStatus(t, v.ID); got.Status != domain.TaxSent || got.ProviderRef == "" {
		t.Fatalf("전송됨: %+v", got)
	}
	e.now = e.now.Add(2 * time.Minute)
	if n, err := w.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("둘째 주기: %d %v", n, err)
	}
	got := e.taxStatus(t, v.ID)
	if got.Status != domain.TaxAccepted || len(got.ApprovalNo) != 24 || got.ApprovalNo[:8] != "20261109" {
		t.Fatalf("국세청 수신: %+v", got)
	}
	if n, _ := w.Tick(context.Background()); n != 0 {
		t.Fatal("끝난 건은 다시 처리하지 않는다")
	}
}

func TestTaxWorkerTransientRetriesThenSucceeds(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("transient", true)
	v := e.taxIssued(t, 100_000)
	for i, wait := range []time.Duration{time.Second, 2 * time.Minute, 6 * time.Minute} { // 백오프 1분·5분 뒤에 세 번째 시도가 성공한다
		e.now = e.now.Add(wait)
		if n, err := w.Tick(context.Background()); err != nil || n != 1 {
			t.Fatalf("%d번째 주기: %d %v", i+1, n, err)
		}
	}
	got := e.taxStatus(t, v.ID)
	if got.Status != domain.TaxSent || got.Attempts != 3 {
		t.Fatalf("재시도 뒤 전송됨: %+v", got)
	}
	var codes []string
	for _, tr := range got.Transmissions {
		codes = append(codes, tr.Code)
	}
	if len(codes) != 3 || codes[0] != "unavailable" || codes[1] != "unavailable" || codes[2] != "ok" {
		t.Fatalf("전송 기록: %v", codes)
	}
}

func TestTaxWorkerPermanentFailsImmediatelyAndNotConfiguredClaimsNothing(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("permanent", true)
	v := e.taxIssued(t, 100_000)
	e.now = e.now.Add(time.Second)
	w.Tick(context.Background())
	got := e.taxStatus(t, v.ID)
	if got.Status != domain.TaxFailed || got.LastCode != "rejected_validation" || got.LastCodeText == "" {
		t.Fatalf("영구 오류는 즉시 실패: %+v", got)
	}
	// 설정이 꺼져 있으면 아무것도 가져가지 않고(발행 대기로 남음), 발행·다시 전송은 한국어 사유로 거부된다.
	w2, _ := e.taxWorker("none", false)
	if _, err := e.tax.Resend(as(uFinance), v.ID); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("대행사 꺼짐에서 다시 전송: %v", err)
	}
	e.tax.ASPReady = nil
	if _, err := e.tax.Resend(as(uFinance), v.ID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	if n, err := w2.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("꺼진 설정은 가져가지 않는다: %d %v", n, err)
	}
	if got := e.taxStatus(t, v.ID); got.Status != domain.TaxIssued || got.Attempts != 0 {
		t.Fatalf("그대로 발행 대기: %+v", got)
	}
}

func TestTaxWorkerDelayedPollsThenAcceptsAndRehearsalStops(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("delayed", true)
	v := e.taxIssued(t, 50_000)
	w.Rehearsal = true
	e.now = e.now.Add(time.Second)
	if n, _ := w.Tick(context.Background()); n != 0 {
		t.Fatal("복구 리허설에서는 어떤 일도 가져가지 않는다")
	}
	w.Rehearsal = false
	// 전송 → 조회(pending) 두 번 → 수신.
	steps := []time.Duration{0, 2 * time.Minute, 6 * time.Minute, 40 * time.Minute, 3 * time.Hour}
	for _, d := range steps {
		e.now = e.now.Add(d)
		w.Tick(context.Background())
	}
	got := e.taxStatus(t, v.ID)
	if got.Status != domain.TaxAccepted {
		t.Fatalf("지연 뒤 수신: %s %+v", got.Status, got.Transmissions)
	}
	pend := 0
	for _, tr := range got.Transmissions {
		if tr.Code == "pending" {
			pend++
		}
	}
	if pend != 2 {
		t.Fatalf("수신 전 조회 2번: %d", pend)
	}
}

func TestTaxWorkerStatusTimeoutAfterSevenDays(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("delayed", true)
	v := e.taxIssued(t, 50_000)
	e.now = e.now.Add(time.Second)
	w.Tick(context.Background())
	// 조회 시각이 와도 계속 pending 인 상태를 만든다: 7일을 넘겨 Tick 하면 status_timeout.
	e.now = e.now.Add(8 * 24 * time.Hour)
	if n, err := w.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	got := e.taxStatus(t, v.ID)
	// 지연 모드의 세 번째 조회부터 수신이므로 위 Tick 에서 이미 수신 처리될 수 있다. 시한 초과 검사는 pending 이 이어지는 경로(StillPending)로 직접 한다.
	if got.Status != domain.TaxAccepted && got.Status != domain.TaxFailed {
		t.Fatalf("상태: %s", got.Status)
	}
	v2 := e.taxIssued(t, 40_000)
	e.now = e.now.Add(time.Second)
	e.tax.ClaimDue(context.Background(), e.now, 10)
	if err := e.tax.Submitted(context.Background(), v2.ID, "R-OLD", 202); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(8 * 24 * time.Hour)
	if err := e.tax.StillPending(context.Background(), v2.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.taxStatus(t, v2.ID); got.Status != domain.TaxFailed || got.LastCode != "status_timeout" {
		t.Fatalf("7일 시한: %+v", got)
	}
}

func TestTaxWorkerPerMinuteCapDefersWithoutFailing(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	w, _ := e.taxWorker("none", true)
	w.PerMin = 2
	var ids []int64
	for i := 0; i < 4; i++ {
		ids = append(ids, e.taxIssued(t, int64(10_000*(i+1))).ID)
	}
	e.now = e.now.Add(time.Second)
	n1, _ := w.Tick(context.Background())
	n2, _ := w.Tick(context.Background()) // 같은 분 안: 한도 소진
	if n1 != 2 || n2 != 0 {
		t.Fatalf("분당 2건: %d %d", n1, n2)
	}
	for i := 0; i < 8; i++ { // 한도 때문에 미뤄진 일은 분이 바뀔 때마다 이어서 처리된다
		e.now = e.now.Add(61 * time.Second)
		w.Tick(context.Background())
	}
	for _, id := range ids {
		if got := e.taxStatus(t, id); (got.Status != domain.TaxSent && got.Status != domain.TaxAccepted) || got.LastCode != "" {
			t.Fatalf("미룬 건도 실패가 아니다: %+v", got)
		}
	}
}

// 동시 워커 두 개가 같은 건을 가져가지 않는다(조건부 갱신).
func TestTaxClaimDueNeverDoubleClaims(t *testing.T) {
	e := newEnv(t)
	e.taxCompany(t)
	for i := 0; i < 5; i++ {
		e.taxIssued(t, int64(1000*(i+1)))
	}
	e.now = e.now.Add(time.Second)
	a, _ := e.tax.ClaimDue(context.Background(), e.now, 3)
	b, _ := e.tax.ClaimDue(context.Background(), e.now, 10)
	if len(a) != 3 || len(b) != 2 {
		t.Fatalf("가져가기: %d %d", len(a), len(b))
	}
	seen := map[int64]bool{}
	for _, j := range append(a, b...) {
		if seen[j.ID] {
			t.Fatalf("같은 건 이중 가져가기: %d", j.ID)
		}
		seen[j.ID] = true
	}
	// 15분 넘게 끝나지 않으면 회수되어 다시 가져갈 수 있다.
	e.now = e.now.Add(16 * time.Minute)
	if again, _ := e.tax.ClaimDue(context.Background(), e.now, 10); len(again) != 5 {
		t.Fatalf("회수: %d", len(again))
	}
}
