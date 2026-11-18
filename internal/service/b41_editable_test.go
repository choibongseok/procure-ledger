package service_test

import "testing"

// 상세 응답의 editable 은 견적 비교의 editable 과 같은 판정(requestEditable) 하나다: 본인의 임시저장·반려만 true, 상신 뒤와 타인은 false.
func TestRequestEditableMatchesQuoteEditable(t *testing.T) {
	e := newEnv(t)
	check := func(label string, id int64, who int64, want bool) {
		t.Helper()
		d, err := e.purchase.Get(as(who), id)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		c, err := e.quotes.Compare(as(who), id)
		if err != nil {
			t.Fatalf("%s 견적: %v", label, err)
		}
		if d.Editable != want || c.Editable != want {
			t.Fatalf("%s: 상세=%v 견적=%v 기대=%v", label, d.Editable, c.Editable, want)
		}
	}
	dr := e.draft(t, uStaff, 1_000)
	check("임시저장", dr.ID, uStaff, true)
	sub := e.submitted(t, uStaff, 1_000)
	check("결재중 본인", sub.ID, uStaff, false)
	check("결재중 같은 부서", sub.ID, uLead, false)
	decideAs(t, e, uLead, sub.ID, false, "견적 보완")
	check("반려 본인", sub.ID, uStaff, true)
	check("반려 같은 부서", sub.ID, uLead, false)
}
