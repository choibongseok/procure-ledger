package handler

import (
	"encoding/json"
	"testing"
)

// 월 예산은 숫자나 쉼표 섞인 문자열로 받는다. 읽지 못하는 값은 ok=false(400)다.
func TestFlexAmountAcceptsCommas(t *testing.T) {
	cases := map[string]struct {
		v  int64
		ok bool
	}{
		`1500000`:     {1_500_000, true},
		`"1,500,000"`: {1_500_000, true},
		`" 2 000 "`:   {2000, true},
		`0`:           {0, true},
		`"abc"`:       {0, false},
		`""`:          {0, false},
		`"1.5"`:       {0, false},
		`null`:        {0, false},
	}
	for in, want := range cases {
		var b struct {
			Amount flexAmount `json:"amount"`
		}
		if err := json.Unmarshal([]byte(`{"amount":`+in+`}`), &b); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if b.Amount.ok != want.ok || (want.ok && b.Amount.v != want.v) {
			t.Errorf("%s → %+v, 기대 %+v", in, b.Amount, want)
		}
	}
}
