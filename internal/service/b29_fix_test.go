package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// AC-814: 폐기된 문서는 번호·양식·기안자로만 찾아진다(제목·본문은 지워졌다).
func TestDisposedDocFoundOnlyByNumberFormRequester(t *testing.T) {
	e := newEnv(t)
	gen := e.completedGen(t, "기밀 제목", "2026-11-09")
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: "보존 연한 만료 정기 정리"}); err != nil {
		t.Fatal(err)
	}
	if !has(e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: gen.No}), gen.ID) {
		t.Error("폐기 문서가 번호로 안 찾아짐")
	}
	if !has(e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "직원", Statuses: []string{"disposed"}}), gen.ID) {
		t.Error("폐기 문서가 기안자로 안 찾아짐")
	}
	if has(e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "기밀"}), gen.ID) {
		t.Error("폐기 문서가 지워진 본문으로 찾아짐")
	}
}

// AC-811·801: 지금 유효한 대결자는 일반 기안서(양식 문서)를 열고 검색하고 결재 대기함에서 본다. 부재가 끝나면 다시 못 본다.
func TestActingDelegateSeesFormDocument(t *testing.T) {
	e := newEnv(t)
	_, docs := e.attach()
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "대결 열람 시험", Fields: map[string]any{"content": "본문"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	inbox := service.NewInboxService(b)
	if _, err := docs.Get(as(uLead3), d.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("부재 등록 전에는 열 수 없어야 함: %v", err)
	}
	dg, err := e.delegation.Create(as(uLead), e.absenceIn(uLead3, e.now, 2, "vacation"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Get(as(uLead3), d.ID); err != nil {
		t.Fatalf("대결자가 문서를 못 엶: %v", err)
	}
	if !has(e.searchIDs(t, uLead3, domain.DocSearchFilter{Query: "대결 열람"}), d.ID) {
		t.Error("대결자의 검색에 안 나옴")
	}
	pend, err := inbox.List(as(uLead3), domain.BoxPending, domain.InboxFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range pend {
		found = found || it.ID == d.ID
	}
	if !found {
		t.Error("대결자의 결재 대기함에 일반 기안서가 없음")
	}
	e.now = dg.To.Add(time.Minute)
	if _, err := docs.Get(as(uLead3), d.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("부재가 끝나면 열 수 없어야 함: %v", err)
	}
}

// 검색 상태 매개변수의 알 수 없는 값은 400, 폐기 문서는 출력할 수 없다.
func TestSearchUnknownStatusAndDisposedPrint(t *testing.T) {
	e := newEnv(t)
	if _, err := e.search.Search(as(uStaff), domain.DocSearchFilter{Query: "시험", Statuses: []string{"bogus"}}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "알 수 없는 상태") {
		t.Fatalf("알 수 없는 상태: %v", err)
	}
	gen := e.completedGen(t, "출력 시험", "2026-11-09")
	_, docs := e.attach()
	if err := docs.RecordPrint(as(uStaff), gen.ID); err != nil {
		t.Fatalf("폐기 전 출력: %v", err)
	}
	if _, err := e.retention.Dispose(as(uAdmin), domain.DisposeInput{IDs: []int64{gen.ID}, Reason: "정리"}); err != nil {
		t.Fatal(err)
	}
	if err := docs.RecordPrint(as(uStaff), gen.ID); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "폐기된 문서") {
		t.Fatalf("폐기 문서 출력: %v", err)
	}
}
