package service_test

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B50 7단계: MCP 팀 부재 조회(AttendanceService.Absence)는 달력과 같은 열람 범위 함수를 쓰되, 본인·조직장·인사 담당에게도 종류를 풀지 않는다.
// 연차·대체휴가는 "휴가", 병가 같은 종류는 "부재" 한 가지이고 사유·구간 이름은 없다.
func TestAttendanceAbsenceScopeCollapsedLabelsAndRange(t *testing.T) {
	a := newEnv(t).attendance(t)
	a.leave.policy(t)
	a.leave.open(t, uStaff, 10)
	a.leave.open(t, uLead, 10)
	a.at("2026-11-10", "10:00")
	a.approvedLeave(t, uStaff, uLead, "annual", "2026-11-16", "2026-11-16")
	up, err := a.leave.att.Upload(as(uStaff), "진단서.pdf", []byte("%PDF-1.4 x"))
	if err != nil {
		t.Fatal(err)
	}
	sick, err := a.leave.svc.Request(as(uStaff), domain.LeaveInput{LeaveCode: "sick", StartDate: "2026-11-18", EndDate: "2026-11-18", ProofAttachmentID: up.ID, Reason: "병명은 적지 않음"})
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, a.env, uLead, sick.DocID, true, "")
	a.org.AddPerson(uFinance, "재무", "과장", unitBuy1, "", "", "") // 같은 팀 동료

	kindsOf := func(uid int64, unit int64) map[string][]string {
		t.Helper()
		v, err := a.svc.Absence(as(uid), "2026-11-01", "2026-11-30", unit)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, p := range v.People {
			for _, e := range p.Entries {
				out[p.Name] = append(out[p.Name], e.Label)
				if e.Part != "" {
					t.Fatalf("구간 이름이 나갔다: %+v", e)
				}
			}
		}
		return out
	}
	// 본인·조직장·동료 모두 종류를 풀지 않는다: 연차는 "휴가", 병가는 "부재".
	for _, who := range []int64{uStaff, uLead, uFinance} {
		got := kindsOf(who, 0)
		if k := got["직원"]; len(k) != 2 || k[0] != "휴가" || k[1] != "부재" {
			t.Fatalf("사용자 %d 가 보는 직원 일정: %v", who, got)
		}
		if _, ok := got["구매팀장2"]; ok {
			t.Fatalf("다른 팀 사람이 있다: %v", got)
		}
	}
	// 인사 담당은 전사 범위여도 종류는 같다.
	hr, err := a.svc.Absence(attHR(), "2026-11-01", "2026-11-30", 0)
	if err != nil || hr.Scope != "전사" {
		t.Fatalf("인사 담당 범위: %+v %v", hr, err)
	}
	for _, p := range hr.People {
		for _, e := range p.Entries {
			if e.Label != "휴가" && e.Label != "부재" {
				t.Fatalf("인사 담당에게 종류가 나갔다: %+v", e)
			}
		}
	}
	// 기간 밖 일정은 없다(사람 목록에도 없다).
	v, err := a.svc.Absence(as(uStaff), "2026-12-01", "2026-12-10", 0)
	if err != nil || len(v.People) != 0 {
		t.Fatalf("기간 밖: %+v %v", v.People, err)
	}
	// 범위 밖 조직을 고르면 빈 결과(존재를 알리지 않는다).
	if v, err := a.svc.Absence(as(uStaff), "2026-11-01", "2026-11-30", unitBuy2); err != nil || len(v.People) != 0 {
		t.Fatalf("범위 밖 조직: %+v %v", v.People, err)
	}
	// 기간 검증: 형식·순서·최대 31일.
	for _, bad := range [][2]string{{"2026/11/01", "2026-11-05"}, {"2026-11-05", "2026-11-01"}, {"2026-11-01", "2026-12-02"}} {
		if _, err := a.svc.Absence(as(uStaff), bad[0], bad[1], 0); !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("잘못된 기간 %v: %v", bad, err)
		}
	}
	if _, err := a.svc.Absence(as(uStaff), "2026-11-01", "2026-12-01", 0); err != nil { // 정확히 31일은 된다
		t.Fatalf("31일: %v", err)
	}
	// 모듈이 꺼지면 없는 것과 같다.
	a.leave.gate.set(false)
	if _, err := a.svc.Absence(as(uStaff), "2026-11-01", "2026-11-30", 0); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("꺼진 모듈: %v", err)
	}
}
