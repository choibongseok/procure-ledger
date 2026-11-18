package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B29: 보존 만료로 폐기된 문서는 도구가 껍데기만 낸다(번호·양식·기안자·기안일·보존 기한·폐기일, 결재 행위).
// 서비스 응답에 제목·입력 칸·결재 의견·첨부 이름·금액이 남아 있어도(최악의 경우) 도구가 한 번 더 내보내지 않는다.

func disposedDoc() domain.PurchaseRequest {
	p := sampleDoc()
	p.DisposedAt = "2026-11-12"
	p.Title = "기밀 인수합병 검토 요청"
	p.RejectReason = "비밀 반려 사유"
	return p
}

func TestGetApprovalDocOfDisposedDocIsShellOnly(t *testing.T) {
	b, d := newDeps()
	d.Docs = stubDocs{docs: map[int64]domain.PurchaseRequest{15: disposedDoc()}}
	for _, who := range []contract.Principal{staffP, auditorP, financeP} {
		res, text := call(t, connect(t, d, who), contract.ToolGetApprovalDoc, map[string]any{"doc_id": 15})
		if res.IsError {
			t.Fatalf("%s: %s", who.Name, text)
		}
		for _, bad := range []string{"기밀 인수합병", "홍길동상사", "복사용지", "회식비", "110-123-456789", "비밀 의견", "비밀 반려", "견적서.pdf", "deadbeefcafe", "1500000", "김참조", "재무팀"} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s: 폐기 문서 응답에 남은 값 %q: %s", who.Name, bad, text)
			}
		}
		m := parseJSON(t, text)
		doc := m["doc"].(map[string]any)
		if m["disposed"] != true || doc["status"] != "폐기됨" || doc["title"] != "폐기된 문서" || doc["disposed_at"] != "2026-11-12" || doc["no"] != "구매팀-협조-2026-0003" || doc["requester"] != "김직원" || doc["form"] != "지출 결의서" {
			t.Fatalf("껍데기: %s", text)
		}
		for _, k := range []string{"amount", "urgent", "reject_reason", "round", "updated_at"} {
			if _, ok := doc[k]; ok {
				t.Fatalf("껍데기에 %q 가 있음: %s", k, text)
			}
		}
		for _, k := range []string{"fields", "recipients", "attachments"} {
			if arr := m[k].([]any); len(arr) != 0 {
				t.Fatalf("%s 는 비어야 함: %s", k, text)
			}
		}
		// 결재 행위(누가·무엇)는 남는다. 의견 칸은 원래 도구 출력에 없다.
		if steps := m["steps"].([]any); len(steps) != 3 || steps[0].(map[string]any)["approver"] != "이팀장" || steps[0].(map[string]any)["status"] != "승인" {
			t.Fatalf("결재 행위: %s", text)
		}
		if !strings.Contains(m["message"].(string), "폐기된 문서입니다") {
			t.Fatal("안내 문구")
		}
	}
	// 폐기되지 않은 같은 문서는 기존처럼 제목과 입력 칸이 나온다(회귀 없음).
	d.Docs = stubDocs{docs: map[int64]domain.PurchaseRequest{15: sampleDoc()}}
	_, text := call(t, connect(t, d, staffP), contract.ToolGetApprovalDoc, map[string]any{"doc_id": 15})
	if m := parseJSON(t, text); m["disposed"] != nil || m["doc"].(map[string]any)["status"] != "결재중" || len(m["fields"].([]any)) == 0 {
		t.Fatalf("폐기되지 않은 문서: %s", text)
	}
	_ = b
}

func TestListMyApprovalDocsShowsDisposedAsShell(t *testing.T) {
	_, d := newDeps()
	in := newStubInbox()
	at := time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC)
	in.items[domain.BoxDone] = []domain.InboxItem{
		{ID: 31, No: "GEN-2020-0001", Title: "기밀 인수합병 검토 요청", FormName: "일반 기안서", FormKind: "general", RequesterName: "김직원", UnitPath: "구매팀",
			Status: domain.PRApproved, Urgent: true, Unread: true, Disposed: true, UpdatedAt: at, Amount: 777000000},
		{ID: 32, No: "GEN-2026-0002", Title: "정상 문서", FormName: "일반 기안서", FormKind: "general", RequesterName: "박직원", Status: domain.PRApproved, UpdatedAt: at, Amount: 5000},
	}
	in.counts[domain.BoxDone] = domain.InboxCount{Total: 2}
	d.Inbox = in
	_, text := call(t, connect(t, d, staffP), contract.ToolListMyApprovalDocs, map[string]any{"box": "done"})
	for _, bad := range []string{"기밀 인수합병", "777000000"} {
		if strings.Contains(text, bad) {
			t.Fatalf("폐기 문서 행에 남은 값 %q: %s", bad, text)
		}
	}
	rows := parseJSON(t, text)["docs"].([]any)
	if len(rows) != 2 {
		t.Fatalf("행: %s", text)
	}
	r0, r1 := rows[0].(map[string]any), rows[1].(map[string]any)
	if r0["status"] != "폐기됨" || r0["title"] != "폐기된 문서" || r0["no"] != "GEN-2020-0001" || r0["requester"] != "김직원" || r0["urgent"] != false || r0["amount"] != nil || r0["unit"] != "" {
		t.Fatalf("폐기 행: %+v", r0)
	}
	if r1["title"] != "정상 문서" || r1["status"] != "승인" || r1["amount"] != float64(5000) {
		t.Fatalf("정상 행은 그대로: %+v", r1)
	}
	if in.marks != 0 {
		t.Fatal("읽음 표시는 바뀌지 않는다")
	}
}
