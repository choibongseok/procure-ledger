package evals

import "testing"

// 골든셋의 기대값은 사람이 적은 숫자라서, 명세의 계산 규칙(입고×발주 단가의 합, 부가세 10% 원 미만 버림, 부가세 0원이면 줄 없음)을
// 독립 계산으로 한 번 더 확인한다. 실제 파이프라인 채점은 erp 의 전표 서비스가 나온 뒤 평가 하니스(journals 묶음)에 연결한다.
func TestJournalGoldenSetIsConsistentWithSpecRules(t *testing.T) {
	s, err := LoadJournalSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cases) != 12 {
		t.Fatalf("케이스 12건이어야 함: %d", len(s.Cases))
	}
	critical, journals := 0, 0
	for _, c := range s.Cases {
		if c.Critical {
			critical++
		}
		var supply int64
		for _, r := range c.PO.Receipt {
			supply += r.Qty * c.PO.Lines[r.Line].UnitPrice
		}
		vat := supply / 10 // 원 미만 버림(정수 나눗셈)
		matches := 0
		for _, d := range c.Docs {
			if d.Confirm == "match" {
				matches++
			}
		}
		for _, j := range c.Expect.Journals {
			journals++
			want := []JournalLine{{"1210", "D", supply}}
			if vat > 0 {
				want = append(want, JournalLine{"1310", "D", vat})
			}
			want = append(want, JournalLine{"2110", "C", supply + vat})
			if len(j.Lines) != len(want) {
				t.Fatalf("%s: 줄 수 %d (기대 %d)", c.ID, len(j.Lines), len(want))
			}
			for i := range want {
				if j.Lines[i] != want[i] {
					t.Fatalf("%s: 줄 %d = %+v (명세 규칙 %+v)", c.ID, i, j.Lines[i], want[i])
				}
			}
			if j.Vendor != c.PO.VendorName || j.Evidence != matches {
				t.Fatalf("%s: 거래처·증빙 수가 다름 %+v", c.ID, j)
			}
		}
	}
	if critical != 2 {
		t.Fatalf("critical 주입 케이스 2건: %d", critical)
	}
	if journals < MinJournalCases-5 { // 전표가 나오는 케이스(대기·무전표 케이스 제외)가 충분히 있어야 한다
		t.Fatalf("전표가 나오는 케이스가 너무 적음: %d", journals)
	}
	// 대기·무전표 케이스는 사유 코드가 있다.
	for _, c := range s.Cases {
		if c.Expect.NoJournal && c.ID != "journal-ai-match-unconfirmed" && len(c.Expect.Queue) != 1 {
			t.Fatalf("%s: 전표가 없는 케이스는 대기 사유가 하나여야 함", c.ID)
		}
	}
}
