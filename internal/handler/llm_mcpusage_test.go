package handler

import (
	"errors"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func TestParseUsagePeriod(t *testing.T) {
	now := time.Date(2026, 11, 10, 15, 30, 0, 0, time.UTC)
	day := func(s string) time.Time { x, _ := time.ParseInLocation("2006-01-02", s, time.UTC); return x }

	// 기본: 오늘을 포함한 최근 7일 [11-04 0시, 11-11 0시)
	from, to, err := parseUsagePeriod("", "", now)
	if err != nil || !from.Equal(day("2026-11-04")) || !to.Equal(day("2026-11-11")) {
		t.Fatalf("기본 기간: %v %v %v", from, to, err)
	}
	// 종료일은 그날 끝까지 포함(다음 날 0시 미만)
	from, to, err = parseUsagePeriod("2026-11-01", "2026-11-03", now)
	if err != nil || !from.Equal(day("2026-11-01")) || !to.Equal(day("2026-11-04")) {
		t.Fatalf("지정 기간: %v %v %v", from, to, err)
	}
	// 하루짜리
	from, to, err = parseUsagePeriod("2026-11-03", "2026-11-03", now)
	if err != nil || to.Sub(from) != 24*time.Hour {
		t.Fatalf("하루: %v %v %v", from, to, err)
	}
	// 한쪽만 주면 나머지는 기본으로 채운다
	if from, _, err = parseUsagePeriod("2026-11-09", "", now); err != nil || !from.Equal(day("2026-11-09")) {
		t.Fatalf("from 만: %v %v", from, err)
	}
	// 잘못된 입력은 검증 오류
	for _, c := range [][2]string{{"abc", ""}, {"", "2026-02-30"}, {"2026-11-05", "2026-11-04"}, {"2025-01-01", "2026-11-10"}, {"2026/11/01", ""}} {
		if _, _, err := parseUsagePeriod(c[0], c[1], now); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%v: 검증 오류여야 함: %v", c, err)
		}
	}
	// 정확히 366일은 허용
	if _, _, err := parseUsagePeriod("2025-11-10", "2026-11-10", now); err != nil {
		t.Errorf("366일은 허용: %v", err)
	}
}
