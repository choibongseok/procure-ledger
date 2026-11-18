package service_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 부재 중인 사람은 위임 후보 목록에 Away 로 표시된다(대결자로 고를 수 없음을 화면이 알린다).
func TestDelegateCandidatesMarkAwayPeople(t *testing.T) {
	e := newEnv(t)
	if _, err := e.delegation.Create(as(uLead3), e.absenceIn(uLead, e.now, 2, "vacation")); err != nil {
		t.Fatal(err)
	}
	cands, err := e.delegation.Candidates(as(uLead))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.ID == uLead3 {
			found = true
			if !c.Away {
				t.Error("부재 중인 후보가 Away 로 표시되지 않음")
			}
		} else if c.Away {
			t.Errorf("부재 중이 아닌 후보 %d 가 Away 로 표시됨", c.ID)
		}
	}
	if !found {
		t.Fatal("후보에 부재 중인 동료가 없음(시험 준비)")
	}
}

// 폐기된 문서의 첨부는 폐기 안내를, 없는 첨부 번호는 일반 찾을 수 없음을 돌려준다.
func TestAttachmentOpenAfterDisposal(t *testing.T) {
	e := newEnv(t)
	gen := e.completedGen(t, "첨부 폐기", "2026-11-09")
	att, _ := e.attach()
	attID, err := strconv.ParseInt(e.col(t, `SELECT id FROM document_attachments WHERE doc_id = ?`, gen.ID), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := att.Open(as(uStaff), attID); err != nil {
		t.Fatalf("폐기 전 열기: %v", err)
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: "정리"}); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Open(as(uStaff), attID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "폐기된 문서입니다") {
		t.Fatalf("폐기 뒤 열기: %v", err)
	}
	if _, err := att.Open(as(uStaff), 99999); !errors.Is(err, contract.ErrNotFound) || strings.Contains(err.Error(), "폐기") {
		t.Fatalf("없는 첨부: %v", err)
	}
}
