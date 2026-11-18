package service_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// AC-220: 일괄 승인에 본인이 기안한 문서가 섞여 있어도 그 건은 거부되고(자기 결재 금지) 다른 건은 처리된다.
func TestStage2BatchRejectsOwnDocument(t *testing.T) {
	e := newEnv(t)
	_, _, ap := e.stage2()
	own := e.genSubmitted(t, uLead, "팀장 본인 기안")
	other := e.genSubmitted(t, uStaff, "직원 기안")
	res, err := ap.DecideBatch(as(uLead), []domain.BatchItem{{DocID: own.ID}, {DocID: other.ID}}, "일괄")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].OK || res[0].Message == "" || !res[1].OK {
		t.Fatalf("본인 기안은 실패, 다른 건은 성공이어야 함: %+v", res)
	}
	got, _ := e.docs().Get(as(uLead), own.ID)
	for _, s := range got.Steps {
		if s.ApproverID == uLead || s.AssigneeID == uLead {
			t.Fatalf("본인 기안 결재선에 본인이 있음: %+v", s)
		}
		if s.Status == domain.StepApproved {
			t.Fatalf("본인 기안이 승인 처리됨: %+v", got.Steps)
		}
	}
}
