package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 목록 쪽 이동: offset+limit 은 id 오름차순 조각을 주고, Count 는 offset·limit·afterId 와 무관하게 같은 조건의 전체 건수다.
func TestPurchaseListOffsetAndCount(t *testing.T) {
	e := newEnv(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, e.submitted(t, uStaff, 500_000).ID)
	}
	all, err := e.purchase.List(as(uStaff), domain.PRFilter{})
	if err != nil || len(all) != 5 {
		t.Fatalf("전체 목록: %v %d", err, len(all))
	}
	page2, err := e.purchase.List(as(uStaff), domain.PRFilter{Limit: 2, Offset: 2})
	if err != nil || len(page2) != 2 || page2[0].ID != ids[2] || page2[1].ID != ids[3] {
		t.Fatalf("2쪽: %v %+v", err, page2)
	}
	if last, _ := e.purchase.List(as(uStaff), domain.PRFilter{Limit: 2, Offset: 4}); len(last) != 1 || last[0].ID != ids[4] {
		t.Fatalf("마지막 쪽: %+v", last)
	}
	if beyond, _ := e.purchase.List(as(uStaff), domain.PRFilter{Limit: 2, Offset: 5}); len(beyond) != 0 {
		t.Fatalf("범위 밖 offset: %+v", beyond)
	}
	// 기존 afterId 커서는 그대로 동작한다.
	if cur, _ := e.purchase.List(as(uStaff), domain.PRFilter{AfterID: ids[1], Limit: 2}); len(cur) != 2 || cur[0].ID != ids[2] {
		t.Fatalf("afterId 호환: %+v", cur)
	}
	for _, f := range []domain.PRFilter{{}, {Limit: 2, Offset: 4}, {AfterID: ids[3], Limit: 1}, {Status: domain.PRSubmitted}} {
		n, err := e.purchase.Count(as(uStaff), f)
		if err != nil || n != 5 {
			t.Fatalf("Count(%+v) = %d, %v", f, n, err)
		}
	}
	if n, _ := e.purchase.Count(as(uStaff), domain.PRFilter{Status: domain.PRDraft}); n != 0 {
		t.Fatalf("상태 조건 건수: %d", n)
	}
	// 부서 격리: 영업 건이 하나 생겨도 구매팀 건수는 그대로고, 영업 직원은 자기 건만 센다.
	e.submitted(t, uStaff2, 500_000)
	if n, _ := e.purchase.Count(as(uLead3), domain.PRFilter{}); n != 5 {
		t.Fatalf("타 부서 건이 건수에 섞임: %d", n)
	}
	if n, _ := e.purchase.Count(as(uStaff2), domain.PRFilter{}); n != 1 {
		t.Fatalf("영업 직원 건수: %d", n)
	}
	if l, _ := e.purchase.List(as(uStaff2), domain.PRFilter{Offset: 1}); len(l) != 0 {
		t.Fatalf("offset 으로 타 부서 건에 닿음: %+v", l)
	}
}

// 상태는 쉼표로 여러 값을 받는다("open,partial"). 단일 값은 그대로고, 건수도 같은 조건을 따른다.
func TestPurchaseOrderListMultiStatus(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		pr := e.submitted(t, uStaff2, 500_000)
		approve(t, e, uLead2, pr.ID)
	}
	for status, want := range map[domain.POStatus]int{"open": 3, "open,partial": 3, "partial,open": 3, "partial": 0, "partial,received": 0, "": 3} {
		l, err := e.inventory.ListPurchaseOrders(as(uStaff2), domain.POFilter{Status: status})
		if err != nil || len(l) != want {
			t.Fatalf("목록 %q: %v %d (기대 %d)", status, err, len(l), want)
		}
		if n, err := e.inventory.CountPurchaseOrders(as(uStaff2), domain.POFilter{Status: status, Limit: 1}); err != nil || n != want {
			t.Fatalf("건수 %q: %v %d (기대 %d)", status, err, n, want)
		}
	}
}

// 발주 목록도 같은 방식이다: offset 조각, 전체 건수, 열람 범위 유지.
func TestPurchaseOrderListOffsetAndCount(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		pr := e.submitted(t, uStaff2, 500_000)
		approve(t, e, uLead2, pr.ID)
	}
	l, err := e.inventory.ListPurchaseOrders(as(uStaff2), domain.POFilter{Limit: 2, Offset: 1})
	if err != nil || len(l) != 2 {
		t.Fatalf("발주 쪽: %v %d", err, len(l))
	}
	if last, _ := e.inventory.ListPurchaseOrders(as(uStaff2), domain.POFilter{Limit: 2, Offset: 2}); len(last) != 1 {
		t.Fatalf("마지막 쪽: %d", len(last))
	}
	if n, err := e.inventory.CountPurchaseOrders(as(uStaff2), domain.POFilter{Limit: 1, Offset: 2}); err != nil || n != 3 {
		t.Fatalf("발주 건수: %d %v", n, err)
	}
	if n, _ := e.inventory.CountPurchaseOrders(as(uLead3), domain.POFilter{}); n != 0 {
		t.Fatalf("범위 밖 발주 건수 노출: %d", n)
	}
}
