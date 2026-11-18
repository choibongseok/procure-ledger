package service_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// AC-103 구매 API 의 쓰기 경로 전부가 양식 문서에 닿지 않는다(읽기와 같은 404). 상태(임시저장·상신·반려)마다 표로 돌린다.
func TestPurchaseWritePathsNeverTouchFormDocuments(t *testing.T) {
	e := newEnv(t)
	draft := e.genDraft(t, uStaff, "임시")
	submitted := e.genSubmitted(t, uStaff, "상신")
	rejected := e.genSubmitted(t, uStaff, "반려")
	decideAs(t, e, uLead, rejected.ID, false, "보완")
	rejected, _ = e.docs().Get(as(uStaff), rejected.ID)
	it := e.item(t, "SKU-ISO", 0, false)
	v := e.vendor(t)
	in := domain.DraftInput{VendorID: v.ID, Title: "HIJACK", Reason: "x", Lines: []domain.PRLineInput{{ItemID: it.ID, Quantity: 1, UnitPrice: 100}}}
	paths := map[string]func(id int64) error{
		"수정":     func(id int64) error { _, err := e.purchase.UpdateDraft(as(uStaff), id, in); return err },
		"상신·재상신": func(id int64) error { _, err := e.purchase.Submit(as(uStaff), id); return err },
		"회수":     func(id int64) error { _, err := e.purchase.Withdraw(as(uStaff), id); return err },
		"조회":     func(id int64) error { _, err := e.purchase.Get(as(uStaff), id); return err },
	}
	for _, d := range []domain.PurchaseRequest{draft, submitted, rejected} {
		before := count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, d.ID)
		for name, fn := range paths {
			if err := fn(d.ID); !errors.Is(err, contract.ErrNotFound) {
				t.Fatalf("%s(%s): 404 가 아님: %v", name, d.Title, err)
			}
		}
		if got, _ := e.docs().Get(as(uStaff), d.ID); got.Title == "HIJACK" || got.Status != d.Status || count(t, e.db, `SELECT COUNT(*) FROM approval_history WHERE pr_id = ?`, d.ID) != before {
			t.Fatalf("문서가 바뀜: %+v", got)
		}
	}
	// 반대 방향: 문서 API 는 구매 품의서를 만지지 못한다.
	pr := e.submitted(t, uStaff, 500_000)
	if _, err := e.docs().Submit(as(uStaff), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("문서 API 가 구매 품의서를 상신함: %v", err)
	}
	if _, err := e.docs().UpdateDraft(as(uStaff), pr.ID, domain.DocDraftInput{FormCode: "GEN", Title: "x"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("문서 API 가 구매 품의서를 수정함: %v", err)
	}
}

// AC-110·111 지출 결의서의 금액은 내역 합계와 같아야 한다(서버 검증, total 키 한국어 문구). 임시저장은 입력 도중이라 허용한다.
func TestExpenseTotalMustEqualItemsSum(t *testing.T) {
	e := newEnv(t)
	att, docs := e.attach()
	ev, err := att.Upload(as(uStaff), "영수증.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields := func(total any) map[string]any {
		return map[string]any{"spend_date": "2026-11-10", "spend_kind": "출장", "payee": "김가나", "items": []any{map[string]any{"name": "택시", "amount": 1000}, map[string]any{"name": "식비", "amount": 2000}},
			"total": total, "evidence": []any{float64(ev.ID)}}
	}
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: fields(9999)})
	if err != nil {
		t.Fatalf("임시저장은 합계가 달라도 허용해야 함: %v", err)
	}
	_, err = docs.Submit(as(uStaff), d.ID)
	fe, ok := service.IsFieldErrors(err)
	if !ok || !strings.Contains(fe.ByKey()["total"], "「금액」은(는) 지출 내역의 합계(3,000원)와 같아야 합니다.") {
		t.Fatalf("합계 불일치 상신: %v", err)
	}
	if got, _ := docs.Get(as(uStaff), d.ID); got.Status != domain.PRDraft {
		t.Fatalf("거부됐는데 상태가 바뀜: %s", got.Status)
	}
	// 합계를 맞추면 상신되고, 결재선 금액은 합계(3,000원)다.
	if _, err := docs.UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: fields(3000)}); err != nil {
		t.Fatal(err)
	}
	pr, err := docs.Submit(as(uStaff), d.ID)
	if err != nil || pr.Amount != 3000 {
		t.Fatalf("합계가 맞으면 상신: %+v %v", pr.Amount, err)
	}
	// 게시 검증: 합계 규칙이 표의 금액 열을 가리키지 않으면 거부한다.
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "EXP")
	def := v.Definition
	for i := range def.Fields {
		if def.Fields[i].Key == "total" {
			def.Fields[i].SumOf = "nothing.amount"
		}
	}
	_, _ = f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def})
	if _, err := f.Publish(as(uAdmin), v.ID); err == nil || !strings.Contains(err.Error(), "합계 규칙은 표 필드의 금액 열을 가리켜야 합니다") {
		t.Fatalf("잘못된 합계 규칙이 게시됨: %v", err)
	}
	// 시드 2판의 기본 수신처는 개발 시드의 재무팀 코드(T-FIN)이고 1판은 퇴역했다.
	d2, _ := f.Get(as(uStaff), "EXP")
	if d2.Version != 2 || len(d2.Definition.Recipients.Receive) != 1 || d2.Definition.Recipients.Receive[0].UnitCode != "T-FIN" {
		t.Fatalf("지출 결의서 2판: %+v", d2.Definition.Recipients)
	}
}

// AC-135 합의 단계는 위임할 수 없다: 합의자의 위임 수임자 결재함에도 그 문서가 올라오지 않는다(결재 판정과 같은 조건).
func TestAgreeStepNotInDelegateInbox(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead3, uCEO) // 구매2팀 팀장(합의자) → 대표
	pr := e.genSubmitted(t, uStaff, "합의 위임 목록", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	list, err := e.approval.ListPending(as(uCEO))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == pr.ID {
			t.Fatal("합의 건이 위임 수임자의 결재함에 보임")
		}
	}
	own, _ := e.approval.ListPending(as(uLead3))
	found := false
	for _, p := range own {
		found = found || p.ID == pr.ID
	}
	if !found {
		t.Fatal("합의자 본인의 결재함에는 보여야 함")
	}
}

// 개인 결재선을 같은 이름으로 다시 저장해도 양식 지정이 지워지지 않는다(입력이 비면 기존 값 보존).
func TestPersonalLineOverwriteKeepsFormCode(t *testing.T) {
	e := newEnv(t)
	p := e.personal()
	edits := []domain.LineEdit{{Role: "cc", TargetType: "user", TargetID: uStaff2}}
	a, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "내 결재선", FormCode: "GEN", Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "내 결재선", Edits: edits})
	if err != nil || b.ID != a.ID || b.FormCode != "GEN" {
		t.Fatalf("덮어쓰기에서 양식 지정이 지워짐: %+v %v", b, err)
	}
	c, _ := p.Save(as(uStaff), domain.PersonalLineInput{Name: "내 결재선", FormCode: "COOP", Edits: edits})
	if c.FormCode != "COOP" {
		t.Fatalf("명시한 양식은 바뀌어야 함: %+v", c)
	}
}

// 첨부 업로드 감사의 대상 종류는 첨부(문서가 아니다).
func TestAttachmentAuditTargetsAttachment(t *testing.T) {
	e := newEnv(t)
	att, _ := e.attach()
	a, err := att.Upload(as(uStaff), "a.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range e.rec.Records() {
		if r.Action == "doc_attachment.upload" && string(r.TargetType) == "doc_attachment" && r.TargetID == fmtID(a.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("첨부 업로드 감사 대상이 첨부가 아님")
	}
}

func fmtID(id int64) string { return fmt.Sprint(id) }
