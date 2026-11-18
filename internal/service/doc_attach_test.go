package service_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) attach() (*service.AttachmentService, *service.ApprovalDocService) {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	docs := service.NewApprovalDocService(b)
	return service.NewAttachmentService(b, docs), docs
}

var (
	pdfBytes = append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 50)...)
	pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{1}, 20)...)
	zipBytes = append([]byte{'P', 'K', 0x03, 0x04}, bytes.Repeat([]byte{2}, 20)...)
)

// AC-154 업로드 검사: 확장자 허용 목록, 파일 크기, 내용 서명(매직 바이트·zip), 파일 이름 정리, 감사에는 이름·크기·해시만.
func TestAttachmentUploadValidation(t *testing.T) {
	e := newEnv(t)
	att, _ := e.attach()
	att.Limits.MaxFileBytes = 1000
	ok := []struct {
		name string
		data []byte
	}{{"계약서.pdf", pdfBytes}, {"사진.PNG", pngBytes}, {"표.xlsx", zipBytes}, {"한글.hwp", []byte{0xD0, 0xCF, 0x11, 0xE0, 1, 2, 3}}, {"메모.txt", []byte("안녕")}}
	for _, c := range ok {
		if _, err := att.Upload(as(uStaff), c.name, c.data); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	bad := map[string]struct {
		name string
		data []byte
		want string
	}{
		"형식":       {"악성.exe", []byte("MZ"), "허용하지 않는 파일 형식입니다(.exe)"},
		"서명 불일치":   {"가짜.png", []byte("MZ\x90\x00"), "파일 내용이 확장자와 맞지 않습니다"},
		"pdf 위조":   {"가짜.pdf", zipBytes, "파일 내용이 확장자와 맞지 않습니다"},
		"txt 바이너리": {"가짜.txt", []byte{0, 1, 2}, "파일 내용이 확장자와 맞지 않습니다"},
		"큰 파일":     {"큰.pdf", append([]byte("%PDF-"), bytes.Repeat([]byte("a"), 2000)...), "파일당"},
		"빈 파일":     {"빈.pdf", nil, "빈 파일"},
	}
	for k, c := range bad {
		_, err := att.Upload(as(uStaff), c.name, c.data)
		if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", k, err)
		}
	}
	// 파일 이름: 경로·제어 문자를 정리한다.
	a, err := att.Upload(as(uStaff), "../../etc/passwd\x00.pdf", pdfBytes)
	if err != nil || a.Filename != "passwd.pdf" {
		t.Fatalf("파일 이름 정리: %q %v", a.Filename, err)
	}
	if got := service.SanitizeFilename(`C:\Users\나\"보고서"?.docx`); got != "Users나보고서.docx" && got != "보고서.docx" {
		// 마지막 경로 요소만 남고 금지 문자가 빠진다.
		if !strings.HasSuffix(got, ".docx") || strings.ContainsAny(got, `\/:"?`) {
			t.Fatalf("파일 이름 정리: %q", got)
		}
	}
	// 감사에는 내용이 없다: 이름·크기·해시 키만.
	for _, r := range e.rec.Records() {
		if r.Action != "doc_attachment.upload" {
			continue
		}
		for _, d := range r.Diff {
			if d.Key != "filename" && d.Key != "size" && d.Key != "sha256" {
				t.Fatalf("감사에 예상 밖 키: %s", d.Key)
			}
		}
	}
	// 기안 권한이 없으면 거부.
	if _, err := att.Upload(as(uAuditor), "a.pdf", pdfBytes); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사자: %v", err)
	}
}

// 첨부는 본인이 올린 것만 문서에 붙고, 붙은 뒤에는 문서 열람 조건을 따르며, 개수·합계 상한이 있고, 붙지 않은 채 24시간이 지나면 정리된다.
func TestAttachmentLinkAccessAndPurge(t *testing.T) {
	e := newEnv(t)
	att, docs := e.attach()
	a1, _ := att.Upload(as(uStaff), "증빙1.pdf", pdfBytes)
	other, _ := att.Upload(as(uLead), "남의.pdf", pdfBytes)
	exp := func(ids ...any) map[string]any {
		return map[string]any{"spend_date": "2026-11-10", "spend_kind": "출장", "payee": "김가나", "items": []any{map[string]any{"name": "택시", "amount": 100}}, "total": 100, "evidence": ids}
	}
	// 남의 첨부는 붙일 수 없다.
	if _, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "x", Fields: exp(float64(other.ID))}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("남의 첨부: %v", err)
	}
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: exp(float64(a1.ID))})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 1 || d.Attachments[0].Filename != "증빙1.pdf" {
		t.Fatalf("첨부 목록: %+v", d.Attachments)
	}
	// 같은 첨부를 다른 문서에 또 붙일 수 없다.
	if _, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "중복", Fields: exp(float64(a1.ID))}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("첨부 재사용: %v", err)
	}
	// 임시저장 문서의 첨부는 기안자만 연다(문서 열람 조건).
	if f, err := att.Open(as(uStaff), a1.ID); err != nil || len(f.Data) == 0 || f.ContentType != "application/pdf" {
		t.Fatalf("본인 열기: %v", err)
	}
	if _, err := att.Open(as(uLead), a1.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남이 임시저장 첨부를 엶: %v", err)
	}
	// 붙지 않은 첨부는 올린 사람만 연다.
	if _, err := att.Open(as(uStaff), other.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 미첨부: %v", err)
	}
	// MCP 채널로는 내용이 나가지 않는다.
	mcp := users[uStaff]
	mcp.Channel = contract.ChannelMCP
	if _, err := att.Open(contractWith(mcp), a1.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("MCP 가 첨부 내용을 엶: %v", err)
	}
	// 개수 상한.
	att.Limits.MaxFiles = 1
	docs.Limits = att.Limits
	b1, _ := att.Upload(as(uStaff), "b1.pdf", pdfBytes)
	b2, _ := att.Upload(as(uStaff), "b2.pdf", pdfBytes)
	if _, err := docs.UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: exp(float64(b1.ID), float64(b2.ID))}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("개수 상한: %v", err)
	}
	// 정리: 24시간 전에 올린 붙지 않은 첨부만 지운다(a1 은 문서에 붙어 있고 b1·b2 는 롤백됐다).
	e.now = e.now.Add(25 * time.Hour)
	n, err := att.PurgeOrphans(as(uStaff))
	if err != nil || n < 2 {
		t.Fatalf("정리 %d %v", n, err)
	}
	if c := count(t, e.db, `SELECT COUNT(*) FROM document_attachments WHERE id = ?`, a1.ID); c != 1 {
		t.Fatal("문서에 붙은 첨부가 정리됨")
	}
	if !e.rec.Has("doc_attachment.purge", "doc_attachment", "success") {
		t.Fatal("정리 감사 기록 없음")
	}
}

func contractWith(p contract.Principal) context.Context {
	return contract.WithPrincipal(context.Background(), p)
}
