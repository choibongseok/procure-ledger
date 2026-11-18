package service

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 휴직 구간의 출근 간주 표와 복원 분량 계산(순수 함수 표 시험).
func TestAbsenceDayRuleTable(t *testing.T) {
	spans := []repository.AbsenceSpan{
		{From: "2026-03-02", To: "2026-03-10", Kind: "childcare"},
		{From: "2026-03-10", To: "2026-03-20", Kind: "illness"},
		{From: "2026-04-01", To: "", Kind: "personal"},
	}
	cases := []struct {
		day              string
		counted, removed bool
	}{
		{"2026-03-01", false, false}, // 휴직 전
		{"2026-03-02", true, false},  // 육아 첫날: 출근 간주
		{"2026-03-09", true, false},  // 육아 마지막 날(To 는 배타)
		{"2026-03-10", false, true},  // 질병 첫날: 소정근로일 제외
		{"2026-03-19", false, true},
		{"2026-03-20", false, false}, // 복직일
		{"2026-04-01", false, true},  // 열린 구간
		{"2030-01-01", false, true},
	}
	for _, c := range cases {
		counted, removed := absenceDayRule(spans, c.day)
		if counted != c.counted || removed != c.removed {
			t.Errorf("%s: 간주 %v 제외 %v(기대 %v %v)", c.day, counted, removed, c.counted, c.removed)
		}
	}
}

func TestAbsenceRestoreTargetTable(t *testing.T) {
	biz := []string{"2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19", "2026-11-20"}
	span := func(from, to string) []repository.AbsenceSpan {
		return []repository.AbsenceSpan{{From: from, To: to, Kind: "illness"}}
	}
	cases := []struct {
		name  string
		spans []repository.AbsenceSpan
		min   int64
		days  []string
		want  int64
	}{
		{"겹침 없음", span("2026-12-01", ""), 2400, biz, 0},
		{"뒤 3일", span("2026-11-18", ""), 2400, biz, 1440},
		{"구간이 전부를 덮음", span("2026-11-01", "2026-12-01"), 2400, biz, 2400},
		{"복직일 배타: 하루", span("2026-11-18", "2026-11-19"), 2400, biz, 480},
		{"하루짜리 반차", span("2026-11-17", ""), 240, []string{"2026-11-17"}, 240},
		{"휴가 기간 밖에서 끝난 휴직", span("2026-11-01", "2026-11-16"), 2400, biz, 0},
		{"소정근로일이 없음", span("2026-11-18", ""), 2400, nil, 0},
		{"분량 0", span("2026-11-18", ""), 0, biz, 0},
	}
	for _, c := range cases {
		if got := absenceRestoreTarget(c.spans, c.min, c.days); got != c.want {
			t.Errorf("%s: %d(기대 %d)", c.name, got, c.want)
		}
	}
}
