package mcp_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 범위 밖(다른 부서) 구매요청은 없는 번호와 같은 문구로 거절된다. 존재 여부가 새지 않는다(AC-45b).
func TestGetPurchaseRequestOutOfScopeLooksLikeMissing(t *testing.T) {
	b, d := newDeps()
	_, pr := seedPR(t, b) // staffP(부서 10)가 올린 건
	otherDept := contract.Principal{UserID: 9, Channel: contract.ChannelMCP, Name: "타부서", Role: contract.RoleStaff, DepartmentID: 20}
	cs := connect(t, d, otherDept)

	res, outText := call(t, cs, contract.ToolGetPurchaseRequest, map[string]any{"pr_id": pr.ID})
	if !res.IsError {
		t.Fatalf("범위 밖 건은 거절이어야 함: %s", outText)
	}
	res2, missingText := call(t, cs, contract.ToolGetPurchaseRequest, map[string]any{"pr_id": 987654})
	if !res2.IsError {
		t.Fatalf("없는 번호는 거절이어야 함: %s", missingText)
	}
	if outText != missingText {
		t.Fatalf("범위 밖과 없는 번호의 문구가 다르다(존재 여부 노출): %q vs %q", outText, missingText)
	}
	// 열람 가능한 호출자(같은 부서)는 단계가 나온다.
	csOwn := connect(t, d, staffP)
	okRes, okText := call(t, csOwn, contract.ToolGetPurchaseRequest, map[string]any{"pr_id": pr.ID})
	if okRes.IsError {
		t.Fatalf("같은 부서는 조회 가능: %s", okText)
	}
}
