package service

import (
	"strings"
	"testing"
)

// B32 소스 검사(AC-1258): 자동으로 환입 요청·개인 사용 처리를 하는 경로가 없다 — 환입 요청 전이는 재무가 부르는 RequestRefund 한 곳이고,
// 알림 작업(Tick)은 알림만 만든다. 카드 내역 상태를 쓰는 SQL 은 카드 저장소 한 파일에만 있다.

func checkNoAutoRefund(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		if !strings.HasPrefix(name, "internal/service/") {
			continue
		}
		if n := strings.Count(f.code, "cardEvRefundRequest"); n > 0 && name != "internal/service/card_flow.go" && name != "internal/service/card_service.go" {
			bad = append(bad, name+": 환입 요청 전이를 card_flow.go 밖에서 쓴다")
		}
	}
	if f, ok := files["internal/service/card_flow.go"]; ok {
		if n := strings.Count(f.code, "cardTransition(r.Status, cardEvRefundRequest)"); n != 1 {
			bad = append(bad, "환입 요청 전이는 RequestRefund 에 정확히 한 번이어야 한다")
		}
		tick := f.code[strings.Index(f.code, "func (s *CardService) Tick("):strings.Index(f.code, "func (s *CardService) headOfHolder(")]
		if strings.Contains(tick, "SetTxnStatus") || strings.Contains(tick, "SetTxnRefund") || strings.Contains(tick, "cardExpense") {
			bad = append(bad, "알림 작업(Tick)이 카드 내역 상태나 전표를 바꾼다")
		}
	}
	return bad
}

func checkCardStatusWritesInRepo(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		for _, l := range f.literals {
			low := strings.ToLower(l.text)
			if strings.Contains(low, "update card_transactions") && name != "internal/repository/card_repo.go" {
				bad = append(bad, name+": 카드 내역 갱신 SQL 이 카드 저장소 밖에 있다")
			}
		}
	}
	return bad
}

func TestCardHasNoAutomaticRefundPath(t *testing.T) {
	if bad := checkNoAutoRefund(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestCardStatusWritesStayInCardRepo(t *testing.T) {
	if bad := checkCardStatusWritesInRepo(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestGuardCardChecksFailOnViolation(t *testing.T) {
	bad := parseAll(map[string]string{"internal/service/x.go": "package s\nfunc f(){ _ = cardEvRefundRequest }",
		"internal/repository/y.go": "package r\nvar q = `UPDATE card_transactions SET status = 'x'`"})
	if len(checkNoAutoRefund(bad)) != 1 || len(checkCardStatusWritesInRepo(bad)) != 1 {
		t.Fatal("우회를 못 잡는다")
	}
}
