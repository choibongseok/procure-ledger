package service

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-532: 납기 준수 배지 경계(90 이상 양호, 70 이상 주의, 그 아래 지연 많음)와 표본 적음(분모 3건 미만), 불일치 10% 경계.
func TestRatingBadgeBoundaries(t *testing.T) {
	on := []struct {
		onTime, denom int
		badge         string
		pct           int
	}{
		{0, 0, domain.RatingNone, -1},
		{1, 1, domain.RatingSmall, 100},
		{2, 2, domain.RatingSmall, 100},
		{3, 3, domain.RatingGood, 100},
		{9, 10, domain.RatingGood, 90},
		{89, 100, domain.RatingWarn, 89},
		{7, 10, domain.RatingWarn, 70},
		{69, 100, domain.RatingBad, 69},
		{0, 5, domain.RatingBad, 0},
	}
	for _, c := range on {
		if b, p := onTimeBadge(c.onTime, c.denom); b != c.badge || p != c.pct {
			t.Errorf("준수율 %d/%d → %s %d%%, 기대 %s %d%%", c.onTime, c.denom, b, p, c.badge, c.pct)
		}
	}
	mis := []struct {
		m, n  int
		badge string
		pct   int
	}{
		{0, 0, domain.RatingNone, -1},
		{1, 2, domain.RatingSmall, 50},
		{0, 3, domain.RatingGood, 0},
		{10, 100, domain.RatingGood, 10},
		{11, 100, domain.RatingBad, 11},
		{1, 3, domain.RatingBad, 33},
	}
	for _, c := range mis {
		if b, p := mismatchBadge(c.m, c.n); b != c.badge || p != c.pct {
			t.Errorf("불일치 %d/%d → %s %d%%, 기대 %s %d%%", c.m, c.n, b, p, c.badge, c.pct)
		}
	}
}
