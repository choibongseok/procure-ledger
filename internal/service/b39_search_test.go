package service_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B39: 통합 검색의 구매요청 절은 목록 API 의 검색어(q)를 쓴다. 제목뿐 아니라 요청 번호 일부로도 찾는다.
func TestPurchaseListQueryMatchesNumberAndTitle(t *testing.T) {
	e := newEnv(t)
	pr := e.purchaseDraftWithEdits(t, uStaff, 1000)
	byNo, err := e.purchase.List(as(uStaff), domain.PRFilter{Query: strings.ToLower(pr.No)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range byNo {
		found = found || r.ID == pr.ID
	}
	if !found {
		t.Fatalf("번호 %q 로 찾지 못함", pr.No)
	}
	byTitle, _ := e.purchase.List(as(uStaff), domain.PRFilter{Query: "참조 시"})
	if len(byTitle) == 0 {
		t.Fatal("제목 일부로 찾지 못함")
	}
	none, _ := e.purchase.List(as(uStaff), domain.PRFilter{Query: "없는검색어zzz"})
	if len(none) != 0 {
		t.Fatalf("없는 검색어인데 %d건", len(none))
	}
}
