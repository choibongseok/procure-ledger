package service_test

import (
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// AC-310 출력은 감사에 document.print 로 남고, 열람 범위 밖 문서는 출력할 수 없다(404). 구매 품의서도 같은 경로다.
func TestRecordPrintAuditsAndRespectsScope(t *testing.T) {
	e := newEnv(t)
	docs := e.docs()
	pr := e.genSubmitted(t, uStaff, "출력 시험")
	if err := docs.RecordPrint(as(uStaff), pr.ID); err != nil {
		t.Fatal(err)
	}
	if !e.rec.Has("document.print", "approval_doc", "success") {
		t.Fatal("출력 감사 기록이 없음")
	}
	if err := docs.RecordPrint(as(uLead3), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("범위 밖 사용자의 출력은 404: %v", err)
	}
	if err := docs.RecordPrint(as(uStaff), 9999); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("없는 번호: %v", err)
	}
	p := e.submitted(t, uStaff, 500_000)
	if err := docs.RecordPrint(as(uStaff), p.ID); err != nil {
		t.Fatalf("구매 품의서 출력: %v", err)
	}
	e.rec.FailWith(errBoom)
	if err := docs.RecordPrint(as(uStaff), pr.ID); !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("감사 실패면 출력도 실패: %v", err)
	}
	e.rec.FailWith(nil)
}
