package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// B47 3단계: 첨부 텍스트 색인. 완료된 결재 문서에 붙은 첨부(PDF·TXT·CSV)의 글이 부모 문서 열람 판정으로 검색되고, 읽지 못하는 형식은 상태만 남는다.

func (k *kenv) completedWithAttachments(t *testing.T, title string, files map[string][]byte, edits ...domain.LineEdit) domain.PurchaseRequest {
	t.Helper()
	att, docs := k.attach()
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: title, Fields: map[string]any{"content": "첨부 시험 본문"}, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		a, err := att.Upload(as(uStaff), name, data)
		if err != nil {
			t.Fatalf("첨부 %s: %v", name, err)
		}
		if _, err := k.db.Exec(`UPDATE document_attachments SET doc_id = ? WHERE id = ?`, d.ID, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	pr, err := docs.Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatalf("상신: %v", err)
	}
	decideAs(t, k.env, uLead, pr.ID, true, "")
	decideAs(t, k.env, uHead, pr.ID, true, "")
	done, err := docs.Get(as(uStaff), pr.ID)
	if err != nil || done.Status != domain.PRApproved {
		t.Fatalf("완료: %v %v", done.Status, err)
	}
	return done
}

func attachFiles() map[string][]byte {
	return map[string][]byte{
		"견적.pdf": kit.KoreanMultiPagePDF([][]string{{"견적서 표지", "공급자는 한빛상사이다"}, {"단가 안내", "볼트 단가는 천이백 원이다"}}, true),
		"메모.txt": []byte("회의 메모\n\n납품 일정은 십이월 말이다"),
		"표.csv":  []byte("품목,수량\n너트,이십\n와셔,삼십"),
		"사진.png": pngBytes,
	}
}

func TestKnowledgeAttachmentsAreIndexedAndFollowDocumentScope(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	pr := k.completedWithAttachments(t, "첨부 문서", attachFiles(), domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	k.drain(t)
	// 첨부가 붙은 PDF 의 쪽·txt 의 문단이 출처로 나온다. 링크는 부모 문서다.
	res, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: "볼트 단가는 천이백 원", Kinds: []string{domain.KnowledgeAttachment}})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("PDF 첨부: %+v %v", res, err)
	}
	h := res.Hits[0]
	if h.Source != "결재 문서 "+pr.No+" · 견적.pdf 2쪽" || h.Link != "/docs/"+itoa(pr.ID) || h.Kind != domain.KnowledgeAttachment {
		t.Fatalf("PDF 출처: %+v", h)
	}
	if got := k.sources(t, as(uStaff), "납품 일정은 십이월"); !anyHas(got, "· 메모.txt 문단 1") {
		t.Fatalf("txt 출처: %v", got)
	}
	if got := k.sources(t, as(uStaff), "와셔 삼십"); !anyHas(got, "· 표.csv") {
		t.Fatalf("csv: %v", got)
	}
	// 읽지 못하는 형식(그림)은 조각이 없고 상태만 남는다. 현황에는 건수만 나온다.
	st, err := k.admin.Status(as(uAdmin))
	if err != nil || st.Docs["attachment"] != 4 || st.States["unsupported"] != 1 || st.States["ready"] < 4 {
		t.Fatalf("색인 현황: %+v %v", st, err)
	}
	// 부모 문서를 열 수 있는 참조 대상은 첨부 글도 찾고, 관계 없는 사람은 못 찾는다(존재도 모른다).
	if got := k.sources(t, as(uStaff2), "볼트 단가는 천이백 원"); !anyHas(got, "견적.pdf 2쪽") {
		t.Fatalf("참조 대상: %v", got)
	}
	for name, who := range map[string]int64{"같은 부서 다른 팀장": uLead3, "구매 전체 열람자": uFinance, "다른 부서 팀장": uLead2} {
		if got := k.sources(t, as(who), "볼트 단가는 천이백 원"); len(got) != 0 {
			t.Fatalf("%s 가 첨부 글을 찾음: %v", name, got)
		}
	}
	// 특권 열람자는 찾고 감사에 특권 건수가 남는다.
	if got := k.sources(t, as(uAuditor), "볼트 단가는 천이백 원"); !anyHas(got, "견적.pdf") {
		t.Fatalf("감사자: %v", got)
	}
	if v := lastDiff(t, e, "knowledge.search", "privileged_count"); v != "1" {
		t.Fatalf("특권 건수: %q", v)
	}
	// 양식을 검색에서 빼면 첨부도 즉시 빠지고, 다시 색인하면 색인에서도 빠진다.
	if err := k.admin.SetFormSearchable(as(uAdmin), "GEN", false); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "볼트 단가는 천이백 원"); len(got) != 0 {
		t.Fatalf("양식 제외 뒤 첨부가 나옴: %v", got)
	}
	if _, _, err := k.admin.Reindex(as(uAdmin), "", ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_docs WHERE source_kind = 'attachment'`); n != 0 {
		t.Fatalf("제외한 양식의 첨부가 색인에 남음: %d", n)
	}
	if err := k.admin.SetFormSearchable(as(uAdmin), "GEN", true); err != nil {
		t.Fatal(err)
	}
	done, skipped, err := k.admin.Reindex(as(uAdmin), "", "attachment")
	if err != nil || done != 4 {
		t.Fatalf("첨부 다시 색인: %d %d %v", done, skipped, err)
	}
	if done, skipped, err = k.admin.Reindex(as(uAdmin), "", "attachment"); err != nil || done != 0 || skipped != 4 {
		t.Fatalf("멱등: %d %d %v", done, skipped, err)
	}
}

// 폐기: 부모 문서가 폐기되면 첨부 조각이 같은 트랜잭션에서 지워지고, 낡은 색인에서도 첨부가 나오지 않는다.
func TestKnowledgeAttachmentsVanishWhenParentIsDisposed(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	pr := k.completedWithAttachments(t, "폐기 시험 첨부", map[string][]byte{"메모.txt": []byte("폐기될 첨부의 비밀 문장입니다")})
	k.drain(t)
	if got := k.sources(t, as(uStaff), "폐기될 첨부의 비밀 문장"); !anyHas(got, "메모.txt") {
		t.Fatalf("색인 뒤: %v", got)
	}
	if _, err := e.db.Exec(`UPDATE purchase_requests SET disposed_at = '2026-11-14' WHERE id = ?`, pr.ID); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "폐기될 첨부의 비밀 문장"); anyHas(got, "메모.txt") {
		t.Fatalf("낡은 색인에서 폐기된 문서의 첨부가 나옴: %v", got)
	}
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(context.Background(), func(tx contract.Tx) error {
		return repository.PurgeApprovalDoc(context.Background(), tx, pr.ID, e.now)
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_docs WHERE source_kind = 'attachment' AND parent_doc_id = ?`, pr.ID); n != 0 {
		t.Fatalf("폐기 뒤 첨부 색인이 남음: %d", n)
	}
	if _, _, err := k.admin.Reindex(as(uStaff), "", "attachment"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 다시 색인: %v", err)
	}
}
