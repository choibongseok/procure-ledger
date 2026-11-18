package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

type stubAttach struct {
	up   func(name string, data []byte) (domain.Attachment, error)
	file domain.AttachmentFile
}

func (s stubAttach) Upload(_ context.Context, name string, data []byte) (domain.Attachment, error) {
	return s.up(name, data)
}
func (s stubAttach) Open(context.Context, int64) (domain.AttachmentFile, error) { return s.file, nil }

// AC-154 내려받기 응답은 항상 내려받기(attachment)이고 nosniff 이며 파일 이름은 RFC 5987 로 인코딩된다. 브라우저가 열어 실행하지 않는다.
func TestAttachmentDownloadHeaders(t *testing.T) {
	h := attachmentHandler{svc: stubAttach{file: domain.AttachmentFile{ID: 3, Filename: "견적서 한글.pdf", ContentType: "application/pdf", Data: []byte("%PDF-x")}}, limit: 1 << 20}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/approval-docs/attachments/{id}", h.download)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/approval-docs/attachments/3", nil))
	if rec.Code != 200 {
		t.Fatalf("상태 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; filename*=UTF-8''") || strings.Contains(got, "견적서") {
		t.Fatalf("Content-Disposition 이 RFC 5987 로 인코딩되지 않음: %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("헤더: %v", rec.Header())
	}
}

// 업로드 본문은 서버가 크기 상한(파일 상한 + 1MB)으로 먼저 자른다.
func TestAttachmentUploadBodyLimit(t *testing.T) {
	called := false
	h := attachmentHandler{svc: stubAttach{up: func(name string, data []byte) (domain.Attachment, error) {
		called = true
		return domain.Attachment{ID: 1, Filename: name, Size: int64(len(data))}, nil
	}}, limit: 1000}
	build := func(n int) *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "a.pdf")
		_, _ = fw.Write(append([]byte("%PDF-"), bytes.Repeat([]byte("x"), n)...))
		_ = mw.Close()
		r := httptest.NewRequest("POST", "/api/approval-docs/attachments", &buf)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		return r
	}
	rec := httptest.NewRecorder()
	h.upload(rec, build(10))
	if rec.Code != http.StatusCreated || !called {
		t.Fatalf("정상 업로드 %d", rec.Code)
	}
	called = false
	rec = httptest.NewRecorder()
	h.upload(rec, build(3<<20)) // 상한(1000)+1MB 를 넘는 본문
	if rec.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("큰 본문이 거부되지 않음: %d called=%v", rec.Code, called)
	}
}
