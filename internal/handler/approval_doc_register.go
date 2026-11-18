// 양식 문서(구매 품의서 외) 라우트(B23, 명세 8.2). 기존 /api/documents 는 증빙 문서 검증이 쓰므로 /api/approval-docs 로 둔다.
package handler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// RegisterApprovalDocs 는 /api/approval-docs... 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterApprovalDocs(r contract.Registrar, d deps.Deps) {
	if d.Docs == nil {
		return
	}
	h := approvalDocHandler{svc: d.Docs}
	r.HandleFunc("POST", "/api/approval-docs/line-preview", contract.PermDraftCreate, h.preview)
	r.HandleFunc("POST", "/api/approval-docs", contract.PermDraftCreate, h.create)
	r.HandleFunc("PUT", "/api/approval-docs/{id}", contract.PermDraftCreate, h.update)
	r.HandleFunc("POST", "/api/approval-docs/{id}/submit", contract.PermDraftCreate, h.submit)
	r.HandleFunc("GET", "/api/approval-docs", contract.PermDraftRead, h.list)
	r.HandleFunc("GET", "/api/approval-docs/{id}", contract.PermDraftRead, h.get)
	r.HandleFunc("POST", "/api/approval-docs/{id}/print", contract.PermDraftRead, h.print)
	r.HandleFunc("POST", "/api/approval-docs/{id}/withdraw", contract.PermDraftCreate, h.withdraw)
	if d.Attachments != nil {
		ah := attachmentHandler{svc: d.Attachments, limit: attachLimit(d.AttachMaxFileBytes)}
		r.HandleFunc("POST", "/api/approval-docs/attachments", contract.PermDraftCreate, ah.upload)
		r.HandleFunc("GET", "/api/approval-docs/attachments/{id}", contract.PermDraftRead, ah.download)
	}
}

type approvalDocHandler struct{ svc domain.ApprovalDocService }

type lineEditDTO struct {
	Role       string `json:"role"`
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Group      int    `json:"group"`
	Name       string `json:"name,omitempty"`
}

type docDraftBody struct {
	FormCode string         `json:"formCode"`
	Title    string         `json:"title"`
	Fields   map[string]any `json:"fields"`
	Urgent   bool           `json:"urgent"`
	Edits    []lineEditDTO  `json:"edits"`
}

func toEdits(in []lineEditDTO) []domain.LineEdit {
	out := make([]domain.LineEdit, len(in))
	for i, e := range in {
		out[i] = domain.LineEdit{Role: e.Role, TargetType: e.TargetType, TargetID: e.TargetID, Group: e.Group}
	}
	return out
}

func (h approvalDocHandler) preview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		FormCode  string         `json:"formCode"`
		Amount    int64          `json:"amount"`
		FieldUnit int64          `json:"fieldUnit"`
		Fields    map[string]any `json:"fields"`
		Edits     []lineEditDTO  `json:"edits"`
	}
	if !decode(w, r, &b) {
		return
	}
	pv, err := h.svc.PreviewLine(r.Context(), domain.DocLineInput{FormCode: b.FormCode, Amount: b.Amount, FieldUnit: b.FieldUnit, Fields: b.Fields, Edits: toEdits(b.Edits)})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toLinePreviewDTO(pv))
}

// fieldErrs 는 필드 검증 오류를 필드 키별 문구로 함께 돌려준다(화면이 각 칸 옆에 보인다).
func writeDocErr(w http.ResponseWriter, err error) {
	if fe, ok := service.IsFieldErrors(err); ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fe.Error(), "fieldErrors": fe.ByKey()})
		return
	}
	writeErr(w, err)
}

func (h approvalDocHandler) create(w http.ResponseWriter, r *http.Request) {
	var b docDraftBody
	if !decode(w, r, &b) {
		return
	}
	pr, err := h.svc.CreateDraft(r.Context(), domain.DocDraftInput{FormCode: b.FormCode, Title: b.Title, Fields: b.Fields, Urgent: b.Urgent, Edits: toEdits(b.Edits)})
	if err != nil {
		writeDocErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPRDTO(pr))
}

func (h approvalDocHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b docDraftBody
	if !decode(w, r, &b) {
		return
	}
	pr, err := h.svc.UpdateDraft(r.Context(), id, domain.DocDraftInput{FormCode: b.FormCode, Title: b.Title, Fields: b.Fields, Urgent: b.Urgent, Edits: toEdits(b.Edits)})
	if err != nil {
		writeDocErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h approvalDocHandler) submit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.Submit(r.Context(), id)
	if err != nil {
		writeDocErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

func (h approvalDocHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos, err := h.dtos(r.Context(), []domain.PurchaseRequest{pr})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtos[0])
}

// stalled 는 응답에 "결재자 없음" 표시(구매요청의 noApprover 와 같은 이름)를 붙인다.
func (h approvalDocHandler) dtos(ctx context.Context, docs []domain.PurchaseRequest) ([]prDTO, error) {
	out := toPRDTOs(docs)
	if sv, ok := h.svc.(interface {
		StalledIDs(context.Context, []domain.PurchaseRequest) (map[int64]bool, error)
	}); ok {
		stalled, err := sv.StalledIDs(ctx, docs)
		if err != nil {
			return nil, err
		}
		for i := range out {
			out[i].NoApprover = stalled[out[i].ID]
		}
	}
	return out, nil
}

func (h approvalDocHandler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	pr, err := h.svc.Withdraw(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPRDTO(pr))
}

// print 는 인쇄 버튼을 눌렀을 때 출력 사실을 감사에 남긴다(구매 품의서 번호도 같은 경로로 받는다).
func (h approvalDocHandler) print(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.RecordPrint(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h approvalDocHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.DocFilter{FormCode: q.Get("form"), Status: domain.PRStatus(q.Get("status"))}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "limit 이 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		f.Limit = n
	}
	list, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos, err := h.dtos(r.Context(), list)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dtos)
}

type attachmentHandler struct {
	svc   domain.AttachmentService
	limit int64
}

// upload 는 multipart 파일 하나를 받는다. 본문 크기는 상한 + 여유 1MB 로 서버가 먼저 자른다(큰 업로드가 메모리에 다 올라오지 않게).
func (h attachmentHandler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "첨부는 파일당 "+strconv.FormatInt(h.limit>>20, 10)+"MB 까지입니다.", http.StatusRequestEntityTooLarge)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "파일을 선택하세요.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, h.limit+1))
	if err != nil {
		http.Error(w, "파일을 읽을 수 없습니다.", http.StatusBadRequest)
		return
	}
	a, err := h.svc.Upload(r.Context(), hdr.Filename, data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": a.ID, "filename": a.Filename, "size": a.Size})
}

// download 는 첨부를 내려받기로만 응답한다(브라우저가 열어 실행하지 않는다). 파일 이름은 RFC 5987 로 인코딩한다.
func (h attachmentHandler) download(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	f, err := h.svc.Open(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

// attachLimit 은 파일당 상한이다. 조립 단계가 설정을 채우지 않은 구성(시험)에서는 기본값 10MB 를 쓴다.
func attachLimit(v int64) int64 {
	if v > 0 {
		return v
	}
	return service.DefaultAttachLimits().MaxFileBytes
}
