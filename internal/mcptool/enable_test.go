package mcptool_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/mcptool"
)

// AC-4808: 읽기 도구는 현재 버전의 시험 실행이 성공해야 켜지고, 새 버전은 시험을 다시 거쳐야 하며(켜진 도구는 새 버전이 즉시 적용), 끄기는 즉시다.
func TestEnableRequiresPassedTestOfCurrentVersion(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	if d.Tested || d.Enabled {
		t.Fatalf("새 도구: %+v", d.ToolView)
	}
	if _, err := e.svc.SetEnabled(manage, d.ID, true); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "시험 실행") {
		t.Fatalf("시험 없이 켜기: %v", err)
	}
	// 저장 전 정의로 한 시험은 표시가 남지 않는다(저장된 버전과 같은 정의여야 한다). 다른 정의의 시험도 마찬가지다.
	other := baseDef()
	other["fields"] = []string{"no"}
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(other), Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(manage, d.ID); got.Tested {
		t.Fatal("다른 정의의 시험이 통과 표시를 남김")
	}
	// 같은 정의의 시험이 성공하면 표시가 남고 켤 수 있다.
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(baseDef()), Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(manage, d.ID); !got.Tested {
		t.Fatal("시험 통과 표시가 없음")
	}
	// 실패한 시험은 표시를 남기지 않는다.
	e.fr.err = contract.ErrForbidden
	m2 := baseDef()
	m2["fields"] = []string{"no", "name"}
	d2 := e.save(m2, d.ID)
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(m2), Input: map[string]any{}}); err == nil {
		t.Fatal("서비스 거부가 통과함")
	}
	e.fr.err = nil
	if got, _ := e.svc.Get(manage, d2.ID); got.Tested {
		t.Fatal("새 버전이 시험 없이 통과 표시를 가짐")
	}
	if _, err := e.svc.SetEnabled(manage, d.ID, true); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("현재 버전(v2) 시험이 없는데 켜짐: %v", err)
	}
	if _, err := e.svc.Test(manage, mcptool.TestInput{Definition: raw(m2), Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	on, err := e.svc.SetEnabled(manage, d.ID, true)
	if err != nil || !on.Enabled {
		t.Fatalf("켜기: %+v %v", on.ToolView, err)
	}
	if _, err := e.svc.SetEnabled(manage, d.ID, true); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 켜짐: %v", err)
	}
	// 켜진 도구의 새 버전은 즉시 적용된다(켠 채로 있다).
	m3 := baseDef()
	m3["fields"] = []string{"no"}
	nv := e.save(m3, d.ID)
	if !nv.Enabled || nv.CurrentVersion != 3 {
		t.Fatalf("켜진 도구의 새 버전: %+v", nv.ToolView)
	}
	off, err := e.svc.SetEnabled(manage, d.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("끄기: %+v %v", off.ToolView, err)
	}
	if _, err := e.svc.SetEnabled(manage, d.ID, false); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("이미 꺼짐: %v", err)
	}
	if _, err := e.svc.SetEnabled(reader, d.ID, true); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한으로 켜기: %v", err)
	}
	for _, a := range []string{"mcptool.enable", "mcptool.disable"} {
		if !e.rec.Has(a, "mcp_tool", "success") {
			t.Fatalf("%s 감사가 없음", a)
		}
	}
	// 깨진 정의는 켤 수 없다(카탈로그 버전이 달라짐).
	cat2, _ := newFake()
	res, _ := cat2.Get("things")
	res.Version = 2
	if _, err := e.mk(cat2).SetEnabled(manage, d.ID, true); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "맞지 않아") {
		t.Fatalf("깨진 정의 켜기: %v", err)
	}
}

func TestSetRate(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	if d.RatePerMin != 30 {
		t.Fatalf("기본 한도 %d", d.RatePerMin)
	}
	for _, bad := range []int{0, -1, 61} {
		if _, err := e.svc.SetRate(manage, d.ID, bad); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("한도 %d: %v", bad, err)
		}
	}
	got, err := e.svc.SetRate(manage, d.ID, 5)
	if err != nil || got.RatePerMin != 5 {
		t.Fatalf("한도 저장: %+v %v", got.ToolView, err)
	}
	if _, err := e.svc.SetRate(manage, d.ID, 5); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("같은 한도: %v", err)
	}
	if _, err := e.svc.SetRate(reader, d.ID, 7); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("읽기 권한: %v", err)
	}
	if !e.rec.Has("mcptool.limit_set", "mcp_tool", "success") {
		t.Fatal("mcptool.limit_set 감사가 없음")
	}
}

// AC-4810: 분 단위 창 — 한도까지만 세고 넘은 호출은 호출 수를 올리지 않으며(거부 수만 센다), 도구·사용자·분이 다르면 따로 센다. 지난 창은 정리된다.
func TestAllowCallWindow(t *testing.T) {
	e := newEnv(t)
	d := e.save(baseDef(), 0)
	txs := contract.SQLTxManager{DB: e.db}
	ctx := manage
	t0 := time.Date(2026, 11, 14, 9, 0, 10, 0, time.UTC)
	allow := func(user int64, at time.Time) bool {
		ok, err := mcptool.AllowCall(ctx, txs, d.ID, user, 2, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !allow(1, t0) || !allow(1, t0.Add(5*time.Second)) {
		t.Fatal("한도 안의 호출이 거부됨")
	}
	if allow(1, t0.Add(10*time.Second)) || allow(1, t0.Add(20*time.Second)) {
		t.Fatal("한도를 넘은 호출이 통과함")
	}
	var calls, denied int
	if err := e.db.QueryRow(`SELECT calls, denied FROM mcp_tool_rate_window WHERE tool_id = ? AND user_id = 1`, d.ID).Scan(&calls, &denied); err != nil || calls != 2 || denied != 2 {
		t.Fatalf("창: calls=%d denied=%d %v", calls, denied, err)
	}
	if !allow(2, t0) {
		t.Fatal("다른 사용자의 창이 같이 세어짐")
	}
	if !allow(1, t0.Add(time.Minute)) {
		t.Fatal("다음 분은 새 창이어야 함")
	}
	// 10분 지난 창은 정리된다.
	allow(1, t0.Add(30*time.Minute))
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_rate_window WHERE minute < '2026-11-14T09:20'`).Scan(&n)
	if n != 0 {
		t.Fatalf("오래된 창 %d개가 남음", n)
	}
}
