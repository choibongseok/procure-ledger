package repository

import "testing"

// AC-901: 이동평균 경계값 표. 입고는 수량×단가, 출고는 잔액 금액의 비율(0.5 올림), 마지막 출고는 잔액 금액 전부, 조정(+)은 평균 단가.
func TestValuateMovingAverage(t *testing.T) {
	st := valState{}
	step := func(name string, in valInput, wantAmt, wantQty, wantBal, wantAvg int64) valOutput {
		t.Helper()
		out := valuate(&st, in)
		if out.amount != wantAmt || st.balQty != wantQty || st.balAmt != wantBal || st.avg != wantAvg {
			t.Fatalf("%s: 금액 %d 잔량 %d 잔액 %d 평균 %d, 기대 %d %d %d %d", name, out.amount, st.balQty, st.balAmt, st.avg, wantAmt, wantQty, wantBal, wantAvg)
		}
		return out
	}
	step("입고 10×100", valInput{qty: 10, reason: "receipt", inCost: 100}, 1000, 10, 1000, 100)
	step("입고 10×200", valInput{qty: 10, reason: "receipt", inCost: 200}, 2000, 20, 3000, 150)
	step("출고 5", valInput{qty: -5, reason: "issue"}, -750, 15, 2250, 150)
	step("조정(−) 3", valInput{qty: -3, reason: "adjust"}, -450, 12, 1800, 150)
	step("마지막까지 출고 12", valInput{qty: -12, reason: "issue"}, -1800, 0, 0, 150) // 잔돈이 남지 않고 평균은 이어 쓴다
	out := step("잔량 0 에서 조정(+) 4", valInput{qty: 4, reason: "adjust"}, 600, 4, 600, 150)
	if out.warned {
		t.Fatal("직전 평균을 이어 쓰면 경고가 아니다")
	}
	// 평균을 모르는 조정(+): 한 번도 입고가 없던 품목은 0 원 + 경고.
	fresh := valState{}
	if o := valuate(&fresh, valInput{qty: 5, reason: "adjust"}); o.amount != 0 || !o.warned || fresh.balQty != 5 {
		t.Fatalf("평균 없는 조정(+): %+v %+v", o, fresh)
	}
}

// 반올림이 금액에 쌓이지 않는다: 100원 3개를 하나씩 내보내면 합이 정확히 100원이다(33+34+33).
func TestValuateRoundingNeverLeavesResidue(t *testing.T) {
	st := valState{}
	valuate(&st, valInput{qty: 3, reason: "receipt", inCost: 0})
	st.balAmt = 100 // 3개 100원(평균 33.33)
	var total int64
	for i := 0; i < 3; i++ {
		o := valuate(&st, valInput{qty: -1, reason: "issue"})
		total += -o.amount
	}
	if total != 100 || st.balAmt != 0 || st.balQty != 0 {
		t.Fatalf("합 %d, 잔액 %d, 잔량 %d", total, st.balAmt, st.balQty)
	}
	// 0.5 는 올림이다.
	if roundDiv(1, 2) != 1 || roundDiv(3, 2) != 2 || roundDiv(1, 3) != 0 || roundDiv(2, 3) != 1 || roundDiv(0, 5) != 0 {
		t.Fatal("roundDiv 경계")
	}
}

// 이동은 같은 금액이 나가고 들어와 품목 전체 잔액·평균이 변하지 않는다.
func TestValuateTransferKeepsBalance(t *testing.T) {
	st := valState{}
	valuate(&st, valInput{qty: 7, reason: "receipt", inCost: 100})
	before := st
	out := valuate(&st, valInput{qty: -3, reason: "transfer_out"})
	paired := -out.amount
	in := valuate(&st, valInput{qty: 3, reason: "transfer_in", paired: &paired})
	if in.amount != paired || st.balQty != before.balQty || st.balAmt != before.balAmt {
		t.Fatalf("이동 뒤 %+v → %+v (입고 금액 %d)", before, st, in.amount)
	}
}
