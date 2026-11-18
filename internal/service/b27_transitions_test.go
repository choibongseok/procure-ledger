package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-910: 월 상태 전이 표의 전이마다 한 건, 표에 없는 전이는 전부 거부한다.
func TestMonthTransitionsTable(t *testing.T) {
	allowed := []struct {
		from string
		ev   monthEvent
		to   string
	}{
		{"", mevClose, domain.MonthClosed},
		{domain.MonthOpen, mevClose, domain.MonthClosed},
		{domain.MonthClosed, mevRequestRelease, domain.MonthReleasePending},
		{domain.MonthReleasePending, mevReleaseOK, domain.MonthOpen},
		{domain.MonthReleasePending, mevReleaseNo, domain.MonthClosed},
	}
	for _, c := range allowed {
		to, err := monthTransition(c.from, c.ev)
		if err != nil || to != c.to {
			t.Errorf("%q + %s → %q %v, 기대 %q", c.from, c.ev, to, err, c.to)
		}
	}
	allEvents := []monthEvent{mevClose, mevRequestRelease, mevReleaseOK, mevReleaseNo}
	states := []string{"", domain.MonthOpen, domain.MonthClosed, domain.MonthReleasePending}
	ok := map[[2]string]bool{}
	for _, c := range allowed {
		ok[[2]string{c.from, string(c.ev)}] = true
	}
	for _, st := range states {
		for _, ev := range allEvents {
			if ok[[2]string{st, string(ev)}] {
				continue
			}
			if _, err := monthTransition(st, ev); !errors.Is(err, contract.ErrInvalidState) {
				t.Errorf("표에 없는 전이 %q + %s 가 허용됨", st, ev)
			}
		}
	}
}

// AC-913: 마감 월 판정은 assertMonthOpen 한 곳이다. 재고를 바꾸는 쓰기 경로(원장 쓰기·출고·실사·입고)가 모두 이것을 부르고, 다른 곳에서 마감 상태를 직접 읽지 않는다.
func TestMonthGuardUsedByAllStockWritePaths(t *testing.T) {
	srcs := repoSources(t)
	need := map[string]int{
		"internal/service/stock_service.go":       2, // 원장 쓰기 함수, 출고 전표
		"internal/service/stock_count_service.go": 3, // 실사 작성·새로 고침·상신
		"internal/service/inventory_service.go":   1, // 입고
	}
	for file, min := range need {
		src, ok := srcs[file]
		if !ok {
			t.Fatalf("%s 를 찾을 수 없음", file)
		}
		if n := strings.Count(src.code, "assertMonthOpen("); n < min {
			t.Errorf("%s: assertMonthOpen 호출 %d회(기대 %d회 이상)", file, n, min)
		}
	}
	// 마감 상태를 읽는 SQL 은 월 마감 저장소(month_close_repo.go)와 마이그레이션에만 있다.
	for name, src := range srcs {
		if name == "internal/repository/month_close_repo.go" || name == "internal/dbutil/pgdialect/pgdialect.go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, l := range src.literals {
			if strings.Contains(strings.ToLower(l.text), "from month_closes") {
				t.Errorf("%s: month_closes 를 직접 읽는다(SQLMonthClose 를 쓴다): %s", name, strings.TrimSpace(l.text))
			}
		}
	}
}
