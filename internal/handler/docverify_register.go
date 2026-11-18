// (internal/handler/docverify*).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
)

const maxUploadBytes = 8 << 20

// RegisterDocVerify 는 증빙 업로드·판정·확정 라우트(/api/documents...)를 등록한다.
// 호출: cmd/server/routes_ai.go. d.Documents 는 domain.DocumentService 이다.
// 권한은 라우트 선언(Registrar)이 강제하고 서비스가 한 번 더 검사한다. 핸들러 안에 역할 비교는 없다.
//
//	POST /api/documents/verify                      document:verify   multipart(file, po_id, receipt_id) 또는 JSON
//	GET  /api/documents/capabilities                document:verify   {"imageUpload": "allowed"|"forbidden_role"|"disabled"}
//	GET  /api/documents/pending-confirm             document:confirm  확정 대기 건수와 최근 몇 건(홈 카드)
//	GET  /api/documents/verdicts                    document:read     최근 판정 목록
//	GET  /api/documents/verdicts/{id}               document:read
//	POST /api/documents/verdicts/{id}/confirm       document:confirm  {"outcome","comment"}
func RegisterDocVerify(r contract.Registrar, d deps.Deps) {
	h := &docHandler{svc: d.Documents}
	r.HandleFunc("POST", "/api/documents/verify", contract.PermDocumentVerify, h.verify)
	r.HandleFunc("GET", "/api/documents/capabilities", contract.PermDocumentVerify, h.capabilities)
	r.HandleFunc("GET", "/api/documents/pending-confirm", contract.PermDocumentConfirm, h.pendingConfirm)
	r.HandleFunc("GET", "/api/documents/verdicts", contract.PermDocumentRead, h.list)
	r.HandleFunc("GET", "/api/documents/verdicts/{id}", contract.PermDocumentRead, h.get)
	r.HandleFunc("POST", "/api/documents/verdicts/{id}/confirm", contract.PermDocumentConfirm, h.confirm)
}

type docHandler struct{ svc domain.DocumentService }

// ---- 응답 DTO(camelCase) ----

type verdictItemDTO struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	PO       string `json:"po"`
	Receipt  string `json:"receipt"`
	Document string `json:"document"`
	Result   string `json:"result"`
	Note     string `json:"note"`
}

type extractedLineDTO struct {
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unitPrice"`
	Amount    int64  `json:"amount"`
}

type extractionDTO struct {
	Kind        string             `json:"kind"`
	Supplier    string             `json:"supplier"`
	BusinessNo  string             `json:"businessNo"`
	Lines       []extractedLineDTO `json:"lines"`
	TotalAmount int64              `json:"totalAmount"`
	// Citations 는 필드별 원문 인용 구간이다(텍스트·PDF 텍스트 경로). 위치는 응답의 sourceText 기준 룬 오프셋이다.
	Citations map[string]citationDTO `json:"citations"`
}

type citationDTO struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Quote string `json:"quote"`
}

type verdictDTO struct {
	ID               int64            `json:"id"`
	DocumentID       int64            `json:"documentId"`
	POID             int64            `json:"poId"`
	ReceiptID        int64            `json:"receiptId"`
	Outcome          string           `json:"outcome"`
	Items            []verdictItemDTO `json:"items"`
	Extraction       extractionDTO    `json:"extraction"`
	ConfirmedBy      int64            `json:"confirmedBy"`
	ConfirmedOutcome string           `json:"confirmedOutcome"`
	CreatedAt        string           `json:"createdAt"`
	SourceText       string           `json:"sourceText"`
}

func toVerdictDTO(v domain.Verdict) verdictDTO {
	o := verdictDTO{ID: v.ID, DocumentID: v.DocumentID, POID: v.POID, ReceiptID: v.ReceiptID, Outcome: string(v.Outcome),
		Items: []verdictItemDTO{}, ConfirmedBy: v.ConfirmedBy, ConfirmedOutcome: string(v.ConfirmedOutcome),
		CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339), SourceText: v.SourceText}
	for _, it := range v.Items {
		o.Items = append(o.Items, verdictItemDTO{it.Field, it.Expected, it.Actual, it.PO, it.Receipt, it.Actual, string(it.Result), it.Note})
	}
	ex := v.Extraction
	o.Extraction = extractionDTO{Kind: string(ex.Kind), Supplier: ex.Supplier, BusinessNo: ex.BusinessNo, TotalAmount: ex.TotalAmount, Lines: []extractedLineDTO{}, Citations: map[string]citationDTO{}}
	for k, c := range ex.Citations {
		o.Extraction.Citations[k] = citationDTO{c.Start, c.End, c.Quote}
	}
	for _, l := range ex.Lines {
		o.Extraction.Lines = append(o.Extraction.Lines, extractedLineDTO{l.Name, l.Quantity, l.UnitPrice, l.Amount})
	}
	return o
}

// ---- 공용 응답 헬퍼(ai 전용 이름) ----

func aiWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// aiWriteErr 는 계약 오류를 HTTP 상태로 옮긴다. 예상 밖 오류의 내용은 응답에 싣지 않는다.
func aiWriteErr(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "서버 오류가 발생했습니다"
	for _, m := range []struct {
		err  error
		code int
	}{
		{contract.ErrUnauthenticated, http.StatusUnauthorized},
		{contract.ErrForbidden, http.StatusForbidden},
		{contract.ErrModelNotAllowed, http.StatusForbidden},
		{contract.ErrNotFound, http.StatusNotFound},
		{contract.ErrValidation, http.StatusBadRequest},
		{contract.ErrInvalidState, http.StatusConflict},
		{contract.ErrQuotaExceeded, http.StatusTooManyRequests},
	} {
		if errors.Is(err, m.err) {
			code, msg = m.code, err.Error()
			break
		}
	}
	aiWriteJSON(w, code, map[string]string{"error": msg})
}

func aiPathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---- 핸들러 ----

type verifyJSON struct {
	Filename  string `json:"filename"`
	Text      string `json:"text"`
	POID      int64  `json:"poId"`
	ReceiptID int64  `json:"receiptId"`
}

func (h *docHandler) parseVerify(r *http.Request) (domain.VerifyInput, error) {
	bad := func(msg string) (domain.VerifyInput, error) {
		return domain.VerifyInput{}, errors.Join(contract.ErrValidation, errors.New(msg))
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxUploadBytes)
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var in verifyJSON
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return bad("요청 본문을 읽을 수 없습니다")
		}
		return domain.VerifyInput{Filename: in.Filename, ContentType: "text/plain", Text: in.Text, POID: in.POID, ReceiptID: in.ReceiptID}, nil
	}
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		return bad("업로드 형식이 올바르지 않습니다")
	}
	poID, _ := strconv.ParseInt(r.FormValue("po_id"), 10, 64)
	rcID, _ := strconv.ParseInt(r.FormValue("receipt_id"), 10, 64)
	in := domain.VerifyInput{POID: poID, ReceiptID: rcID}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		if txt := r.FormValue("text"); txt != "" {
			in.Text, in.Filename, in.ContentType = txt, "붙여넣기.txt", "text/plain"
			return in, nil
		}
		return bad("파일이 필요합니다")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes))
	if err != nil {
		return bad("파일을 읽을 수 없습니다")
	}
	in.Filename = filepath.Base(hdr.Filename)
	ctype := hdr.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ctype, "image/"):
		in.Image, in.ContentType = data, ctype
	case ctype == docverify.PDFContentType || strings.HasSuffix(strings.ToLower(in.Filename), ".pdf"):
		// 서비스가 텍스트 레이어를 읽어 텍스트 경로로 보낸다. 텍스트가 없는 PDF 는 판단 보류가 된다.
		in.Image, in.ContentType = data, docverify.PDFContentType
	default:
		in.Text, in.ContentType = string(data), "text/plain"
	}
	return in, nil
}

func (h *docHandler) verify(w http.ResponseWriter, r *http.Request) {
	in, err := h.parseVerify(r)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	v, err := h.svc.Verify(r.Context(), in)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, toVerdictDTO(v))
}

func (h *docHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		aiWriteErr(w, contract.ErrNotFound)
		return
	}
	v, err := h.svc.GetVerdict(r.Context(), id)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, toVerdictDTO(v))
}

// verdictLister 는 구현체가 제공하면 목록을 낸다(계약 밖 보조 조회).
type verdictLister interface {
	ListVerdicts(ctx context.Context, limit int) ([]domain.Verdict, error)
}

// pendingConfirmer 는 구현체가 제공하면 확정 대기 요약을 낸다(계약 밖 보조 조회).
type pendingConfirmer interface {
	PendingConfirmations(ctx context.Context, limit int) (int, []domain.Verdict, error)
}

type pendingItemDTO struct {
	ID          int64  `json:"id"`
	POID        int64  `json:"poId"`
	Outcome     string `json:"outcome"`
	Supplier    string `json:"supplier"`
	TotalAmount int64  `json:"totalAmount"`
	CreatedAt   string `json:"createdAt"`
}

func (h *docHandler) pendingConfirm(w http.ResponseWriter, r *http.Request) {
	pc, ok := h.svc.(pendingConfirmer)
	if !ok {
		aiWriteJSON(w, http.StatusOK, map[string]any{"total": 0, "items": []pendingItemDTO{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	total, vs, err := pc.PendingConfirmations(r.Context(), limit)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	items := make([]pendingItemDTO, 0, len(vs))
	for _, v := range vs {
		items = append(items, pendingItemDTO{v.ID, v.POID, string(v.Outcome), v.Extraction.Supplier, v.Extraction.TotalAmount, v.CreatedAt.UTC().Format(time.RFC3339)})
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": items})
}

func (h *docHandler) list(w http.ResponseWriter, r *http.Request) {
	l, ok := h.svc.(verdictLister)
	if !ok {
		aiWriteJSON(w, http.StatusOK, map[string]any{"verdicts": []verdictDTO{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	vs, err := l.ListVerdicts(r.Context(), limit)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	out := make([]verdictDTO, 0, len(vs))
	for _, v := range vs {
		out = append(out, toVerdictDTO(v))
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{"verdicts": out})
}

func (h *docHandler) confirm(w http.ResponseWriter, r *http.Request) {
	id, ok := aiPathID(r)
	if !ok {
		aiWriteErr(w, contract.ErrNotFound)
		return
	}
	var in struct {
		Outcome string `json:"outcome"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		aiWriteErr(w, errors.Join(contract.ErrValidation, errors.New("요청 본문을 읽을 수 없습니다")))
		return
	}
	v, err := h.svc.Confirm(r.Context(), id, domain.ConfirmInput{Outcome: domain.Outcome(in.Outcome), Comment: in.Comment})
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, toVerdictDTO(v))
}

// imageStatuser 는 구현체가 제공하면 이미지 업로드 가능 상태를 낸다(계약 밖 보조 조회).
type imageStatuser interface {
	ImageUploadStatus(ctx context.Context) (string, error)
}

func (h *docHandler) capabilities(w http.ResponseWriter, r *http.Request) {
	st := "disabled" // 구현체가 상태를 모르면 이미지는 막힌 것으로 안내한다(fail-closed)
	if s, ok := h.svc.(imageStatuser); ok {
		v, err := s.ImageUploadStatus(r.Context())
		if err != nil {
			aiWriteErr(w, err)
			return
		}
		st = v
	}
	aiWriteJSON(w, http.StatusOK, map[string]string{"imageUpload": st})
}
