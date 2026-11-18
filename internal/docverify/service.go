package docverify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

const (
	maxTextBytes  = 200 << 10
	maxImageBytes = 5 << 20
)

// VendorSource 는 거래처 조회다(사업자번호 대조용, 읽기 전용).
type VendorSource interface {
	Vendor(ctx context.Context, id int64) (domain.Vendor, error)
}

// SQLVendors 는 vendors 테이블을 읽는 VendorSource 다.
type SQLVendors struct{ DB *sql.DB }

// Vendor 는 VendorSource 를 구현한다.
func (v SQLVendors) Vendor(ctx context.Context, id int64) (domain.Vendor, error) {
	var out domain.Vendor
	err := v.DB.QueryRowContext(ctx, `SELECT id, name, business_no, phone, email FROM vendors WHERE id = ?`, id).
		Scan(&out.ID, &out.Name, &out.BusinessNo, &out.Phone, &out.Email) // 계좌번호는 읽지 않는다(증빙 대조에 쓰지 않고, 암호화 저장 값이다)
	if errors.Is(err, sql.ErrNoRows) {
		return out, contract.ErrNotFound
	}
	return out, err
}

// Service 는 domain.DocumentService 를 구현한다.
type Service struct {
	DB        *sql.DB
	Txs       contract.TxManager
	Audit     audit.Recorder
	LLM       llm.Client
	Inventory domain.InventoryService
	Vendors   VendorSource
	Masker    contract.Masker
	// Privacy 는 개인정보 원문 열람 기록기다(B41). 사업자번호 원문이 응답에 실리는 판정 응답이 응답 전에 부른다. nil 이면 기록 없이 통과한다(서버 조립은 항상 꽂는다).
	Privacy privacy.Noter
	Now     func() time.Time
	// JournalHook 은 판정 확정 때 자동 매입 전표(또는 전표 대기 행)를 만드는 B31 훅이다(erp 전표 서비스). nil 이면 호출하지 않는다(전표 기능 이전 구성).
	JournalHook domain.PurchaseJournalHook
}

var _ domain.DocumentService = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// denied 는 권한 거부를 감사 로그에 남긴다(로그인한 사용자만).
func (s *Service) denied(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id string) {
	if p.UserID == 0 {
		return
	}
	if id == "" {
		id = "0" // 실제 감사 기록기는 대상 ID 가 비면 거부한다. 대상이 아직 없는 거부는 0 으로 남긴다.
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: tt, TargetID: id, Result: audit.ResultDenied})
	}); err != nil {
		log.Printf("docverify: 거부 감사 기록 실패(%s): %v", action, err)
	}
}

// isImage 는 이미지로 LLM 에 보내질 입력인지 본다(PDF 원본 바이트는 제외).
func isImage(in domain.VerifyInput) bool {
	return len(in.Image) > 0 && in.ContentType != PDFContentType
}

// 이미지 업로드 가능 상태. 화면이 이미지 선택을 보일지 안내 문구로 바꿀지 정하는 데 쓴다.
const (
	ImageAllowed       = "allowed"
	ImageForbiddenRole = "forbidden_role"
	ImageDisabled      = "disabled"
)

// ImageUploadStatus 는 현재 사용자가 이미지 증빙을 올릴 수 있는지 알려 준다(권한 → 관리자 설정 순).
func (s *Service) ImageUploadStatus(ctx context.Context) (string, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentVerify)
	if err != nil {
		return "", err
	}
	switch {
	case !p.Can(contract.PermDocumentUploadImage):
		return ImageForbiddenRole, nil
	case !llmgw.ImageTransmissionEnabled(ctx, s.DB):
		return ImageDisabled, nil
	}
	return ImageAllowed, nil
}

// imageBlocked 는 설정이 꺼져 이미지를 거부한 사실을 감사 로그에 남긴다.
func (s *Service) imageBlocked(ctx context.Context, p contract.Principal, poID int64) {
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: llmgw.ActionImageBlocked, TargetType: audit.TargetDocument, TargetID: idStr(poID),
			Result: audit.ResultDenied, Diff: []audit.DiffItem{{Key: "po_id", New: idStr(poID)}, {Key: "reason", New: "disabled"}}})
	}); err != nil {
		log.Printf("docverify: 이미지 차단 감사 기록 실패: %v", err)
	}
}

// Verify 는 domain.DocumentService 를 구현한다.
func (s *Service) Verify(ctx context.Context, in domain.VerifyInput) (domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentVerify)
	if err != nil {
		s.denied(ctx, p, "document.verify", audit.TargetDocument, "")
		return domain.Verdict{}, err
	}
	if in.POID <= 0 {
		return domain.Verdict{}, fmt.Errorf("%w: 대조할 발주가 필요합니다", contract.ErrValidation)
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Image) == 0 {
		return domain.Verdict{}, fmt.Errorf("%w: 문서 내용이 필요합니다", contract.ErrValidation)
	}
	if len(in.Text) > maxTextBytes || len(in.Image) > maxImageBytes {
		return domain.Verdict{}, fmt.Errorf("%w: 문서가 너무 큽니다", contract.ErrValidation)
	}
	// 이미지는 마스킹 없이 LLM 으로 나가므로 재무 권한과 관리자 설정이 모두 필요하다(PDF 는 텍스트로 바뀌어 마스킹되므로 해당 없음).
	if isImage(in) {
		if _, err := contract.Authorize(ctx, contract.PermDocumentUploadImage); err != nil {
			s.denied(ctx, p, "document.upload_image", audit.TargetDocument, idStr(in.POID))
			return domain.Verdict{}, fmt.Errorf("%w: 이미지 증빙은 재무 담당만 올릴 수 있습니다", contract.ErrForbidden)
		}
		if !llmgw.ImageTransmissionEnabled(ctx, s.DB) {
			s.imageBlocked(ctx, p, in.POID)
			return domain.Verdict{}, fmt.Errorf("%w: %s", contract.ErrForbidden, llmgw.ImageBlockedMessage)
		}
	}

	po, err := s.Inventory.GetPurchaseOrder(ctx, in.POID)
	if err != nil {
		return domain.Verdict{}, err
	}
	var rc *domain.Receipt
	if in.ReceiptID != 0 {
		r, err := s.Inventory.GetReceipt(ctx, in.ReceiptID)
		if err != nil {
			return domain.Verdict{}, err
		}
		if r.POID != po.ID {
			return domain.Verdict{}, fmt.Errorf("%w: 입고가 이 발주의 것이 아닙니다", contract.ErrValidation)
		}
		rc = &r
	}
	vendorNo := ""
	if v, err := s.Vendors.Vendor(ctx, po.VendorID); err == nil {
		vendorNo = v.BusinessNo
	} else if !errors.Is(err, contract.ErrNotFound) {
		return domain.Verdict{}, err
	}

	// PDF: 텍스트 레이어를 코드로 읽어 기존 텍스트 경로(게이트웨이 마스킹 포함)로 보낸다. 원본은 보관한다.
	var pdfRaw []byte
	var pdfErr error
	if in.ContentType == PDFContentType {
		pdfRaw, in.Image = in.Image, nil
		if len(pdfRaw) == 0 {
			return domain.Verdict{}, fmt.Errorf("%w: PDF 내용이 비어 있습니다", contract.ErrValidation)
		}
		pdfErr = nil
		if txt, perr := ExtractPDFText(pdfRaw); perr == nil {
			in.Text = txt
		} else if errors.Is(perr, ErrNoTextLayer) || errors.Is(perr, ErrUnreadableText) {
			pdfErr = perr
		} else {
			return domain.Verdict{}, fmt.Errorf("%w: %v", contract.ErrValidation, perr)
		}
	}

	// 추출(LLM). 한도·모델 거부는 그대로 돌려주고(429·403), 그 밖의 실패는 보류로 떨어뜨린다.
	// 텍스트 레이어가 없는 PDF 는 LLM 을 부르지 않고 추측 없이 보류한다.
	var ex domain.Extraction
	extractJSON := "{}"
	var extractErr error
	if pdfErr != nil {
		extractErr = pdfErr
	} else {
		ex, extractJSON, extractErr = s.extract(ctx, in)
	}
	if errors.Is(extractErr, contract.ErrQuotaExceeded) || errors.Is(extractErr, contract.ErrModelNotAllowed) ||
		errors.Is(extractErr, contract.ErrAuditFailed) || errors.Is(extractErr, contract.ErrForbidden) || errors.Is(extractErr, contract.ErrUnauthenticated) {
		return domain.Verdict{}, extractErr
	}

	var outcome domain.Outcome
	var items []domain.VerdictItem
	if extractErr != nil {
		outcome = domain.OutcomeHold
		note := "문서에서 값을 확정해 읽지 못했습니다. 사람이 원본을 확인하세요: " + reason(extractErr)
		var ug *UngroundedError
		if errors.As(extractErr, &ug) {
			note = "AI 가 읽은 값 중 원문에서 근거를 확인하지 못한 항목이 있어 판단을 보류합니다(" + strings.Join(ug.Values, ", ") + "). 사람이 원본과 대조하세요."
		}
		if errors.Is(extractErr, ErrNoTextLayer) {
			note = "텍스트 레이어가 없는 PDF 입니다(스캔본 등). 값을 추측하지 않고 판단을 보류합니다. 이미지로 올리거나 사람이 원본을 확인하세요."
		}
		if errors.Is(extractErr, ErrUnreadableText) {
			note = "PDF 의 글자를 읽을 수 없습니다(글꼴 정보가 없어 한글이 깨짐). 깨진 글자로 판정하지 않고 판단을 보류합니다. 이미지로 올리거나 사람이 원본을 확인하세요."
		}
		items = []domain.VerdictItem{{Field: "extraction", Expected: "스키마에 맞는 추출 결과", Actual: "없음", PO: NA, Receipt: NA, Result: domain.OutcomeHold, Note: note}}
	} else {
		outcome, items = Compare(CompareInput{Extraction: ex, PO: po, Receipt: rc, VendorBusinessNo: vendorNo})
		if isImage(in) {
			// 이미지는 읽을 원문 텍스트가 없어 추출값이 원문에 있는지 교차 확인(Ground)을 할 수 없다. 판정은 바꾸지 않고 근거에 드러낸다.
			items = append(items, domain.VerdictItem{Field: "grounding", Expected: "추출값이 원문에 있는지 교차 확인", Actual: "이미지는 원문 텍스트가 없어 확인하지 못함", PO: NA, Receipt: NA,
				Result: domain.OutcomeHold, Note: "이미지 증빙은 AI 가 읽은 값을 원문과 대조하지 못했습니다. 금액·수량을 사람이 원본 이미지와 직접 확인하세요."})
		}
	}

	kind := ex.Kind
	if kind == "" {
		kind = DetectKind(in.Text, in.Filename)
	}
	stored := in.Image
	if pdfRaw != nil {
		stored = pdfRaw
	}
	sum := sha256.Sum256(append([]byte(in.Text), stored...))
	itemsJSON, _ := json.Marshal(verdictItemsToJSON(items))
	now := s.now().Format(time.RFC3339)

	var v domain.Verdict
	err = s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var textArg, imgArg any
		if in.Text != "" {
			textArg = in.Text
		}
		if len(stored) > 0 {
			imgArg = stored
		}
		var rcArg any
		if in.ReceiptID != 0 {
			rcArg = in.ReceiptID
		}
		ct := in.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		docID, err := dbutil.InsertID(ctx, tx, `INSERT INTO documents (kind, filename, content_type, text_content, image, sha256, uploaded_by, department_id, po_id, receipt_id, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, string(kind), in.Filename, ct, textArg, imgArg, hex.EncodeToString(sum[:]), p.UserID, p.DepartmentID, po.ID, rcArg, now)
		if err != nil {
			return fmt.Errorf("문서 저장: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_extractions (document_id, extraction_json, created_at) VALUES (?,?,?)`, docID, extractJSON, now); err != nil {
			return fmt.Errorf("추출 저장: %w", err)
		}
		vid, err := dbutil.InsertID(ctx, tx, `INSERT INTO document_verdicts (document_id, po_id, receipt_id, outcome, items_json, created_at) VALUES (?,?,?,?,?,?)`,
			docID, po.ID, rcArg, string(outcome), string(itemsJSON), now)
		if err != nil {
			return fmt.Errorf("판정 저장: %w", err)
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "document.upload", TargetType: audit.TargetDocument, TargetID: idStr(docID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "po_id", New: idStr(po.ID)}, {Key: "sha256", New: hex.EncodeToString(sum[:8])}}}); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "document.verify", TargetType: audit.TargetVerdict, TargetID: idStr(vid), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "outcome", New: string(outcome)}}}); err != nil {
			return err
		}
		v = domain.Verdict{ID: vid, DocumentID: docID, POID: po.ID, ReceiptID: in.ReceiptID, Extraction: ex, Outcome: outcome, Items: items,
			CreatedAt: s.now()}
		if len(in.Image) == 0 { // 원문 텍스트가 있는 경로만. 이미지는 원문이 없어 인용도 없다.
			v.SourceText = in.Text
		}
		return nil
	})
	if err != nil {
		return domain.Verdict{}, err
	}
	return s.presentOne(ctx, p, v)
}

func reason(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// extract 는 LLM 으로 값을 추출하고 스키마로 검증한다. 사업자번호는 텍스트 문서면 코드가 원문에서 읽는다
// (LLM 에는 마스킹된 텍스트가 가기 때문). 반환 json 은 저장용이다.
func (s *Service) extract(ctx context.Context, in domain.VerifyInput) (domain.Extraction, string, error) {
	ct := in.ContentType
	if !strings.HasPrefix(ct, "image/") {
		ct = ""
	}
	resp, err := s.LLM.Complete(ctx, BuildExtractRequest(in.Text, in.Image, ct))
	if err != nil {
		return domain.Extraction{}, "{}", err
	}
	ex, hints, err := parseExtraction(resp.Text)
	if err != nil {
		return domain.Extraction{}, "{}", err
	}
	if len(in.Image) == 0 { // 원문 텍스트가 있는 경로(텍스트·PDF 텍스트)만. 이미지는 원문이 없어 인용 없음.
		view, ok := buildMaskView(s.Masker, in.Text)
		if !ok {
			return domain.Extraction{}, "{}", &UngroundedError{Values: []string{"마스킹 위치 대응 실패"}}
		}
		if err := groundView(&ex, hints, view); err != nil {
			return domain.Extraction{}, "{}", err
		}
	} else {
		ex.Citations = nil
	}
	if no := BusinessNoFromText(in.Text); no != "" {
		ex.BusinessNo = no
		if c, ok := BusinessNoSpan(in.Text); ok && ex.Citations != nil {
			ex.Citations["business_no"] = c
		}
	}
	b, _ := json.Marshal(extractionToJSON(ex))
	return ex, string(b), nil
}

type jsonLine struct {
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
	Amount    int64  `json:"amount"`
}

type jsonCite struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Quote string `json:"quote"`
}

type jsonExtraction struct {
	Kind        string              `json:"kind"`
	Supplier    string              `json:"supplier"`
	BusinessNo  string              `json:"business_no"`
	Lines       []jsonLine          `json:"lines"`
	TotalAmount int64               `json:"total_amount"`
	Citations   map[string]jsonCite `json:"citations,omitempty"`
}

func extractionToJSON(ex domain.Extraction) jsonExtraction {
	out := jsonExtraction{Kind: string(ex.Kind), Supplier: ex.Supplier, BusinessNo: ex.BusinessNo, TotalAmount: ex.TotalAmount}
	for _, l := range ex.Lines {
		out.Lines = append(out.Lines, jsonLine{l.Name, l.Quantity, l.UnitPrice, l.Amount})
	}
	for k, c := range ex.Citations {
		if out.Citations == nil {
			out.Citations = map[string]jsonCite{}
		}
		out.Citations[k] = jsonCite{c.Start, c.End, c.Quote}
	}
	return out
}

func extractionFromJSON(s string) domain.Extraction {
	var j jsonExtraction
	if json.Unmarshal([]byte(s), &j) != nil {
		return domain.Extraction{}
	}
	ex := domain.Extraction{Kind: domain.DocKind(j.Kind), Supplier: j.Supplier, BusinessNo: j.BusinessNo, TotalAmount: j.TotalAmount}
	for _, l := range j.Lines {
		ex.Lines = append(ex.Lines, domain.ExtractedLine{Name: l.Name, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Amount})
	}
	for k, c := range j.Citations {
		if ex.Citations == nil {
			ex.Citations = map[string]domain.Citation{}
		}
		ex.Citations[k] = domain.Citation{Start: c.Start, End: c.End, Quote: c.Quote}
	}
	return ex
}

type jsonItem struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	PO       string `json:"po,omitempty"`
	Receipt  string `json:"receipt,omitempty"`
	Result   string `json:"result"`
	Note     string `json:"note"`
}

func verdictItemsToJSON(items []domain.VerdictItem) []jsonItem {
	out := make([]jsonItem, 0, len(items))
	for _, it := range items {
		out = append(out, jsonItem{it.Field, it.Expected, it.Actual, it.PO, it.Receipt, string(it.Result), it.Note})
	}
	return out
}

// presentSource 는 원문과 인용 위치를 역할별 마스킹에 맞춰 돌려준다. mask:bypass 가 없으면 원문을 가리고,
// 인용 위치는 가려진 원문 위에서 다시 계산한다. 가려진 원문과 맞지 않는 인용은 버린다(강조 위치가 어긋나지 않게).
func (s *Service) presentSource(p contract.Principal, text string, cites map[string]domain.Citation) (string, map[string]domain.Citation) {
	if text == "" || contract.RawFieldAllowed(p) {
		return text, cites
	}
	masked := s.Masker.MaskText(text)
	if cites == nil {
		return masked, nil
	}
	raw, ms := []rune(text), []rune(masked)
	out := map[string]domain.Citation{}
	for k, c := range cites {
		if c.Start < 0 || c.End > len(raw) || c.Start >= c.End {
			continue
		}
		q := s.Masker.MaskText(string(raw[c.Start:c.End]))
		start := len([]rune(s.Masker.MaskText(string(raw[:c.Start]))))
		end := start + len([]rune(q))
		if end <= len(ms) && string(ms[start:end]) == q {
			out[k] = domain.Citation{Start: start, End: end, Quote: q}
		}
	}
	return masked, out
}

// present 는 응답용 마스킹이다: 사업자번호는 mask:bypass 가 없으면 가린다.
func (s *Service) present(p contract.Principal, v domain.Verdict) domain.Verdict {
	if s.Masker == nil {
		return v
	}
	v.SourceText, v.Extraction.Citations = s.presentSource(p, v.SourceText, v.Extraction.Citations)
	v.Extraction.BusinessNo = s.Masker.MaskField(p, contract.FieldBusinessNo, v.Extraction.BusinessNo)
	items := make([]domain.VerdictItem, len(v.Items))
	for i, it := range v.Items {
		if it.Field == "business_no" {
			it.Expected = s.Masker.MaskField(p, contract.FieldBusinessNo, it.Expected)
			it.Actual = s.Masker.MaskField(p, contract.FieldBusinessNo, it.Actual)
		}
		items[i] = it
	}
	v.Items = items
	return v
}

// presentAll 은 판정들을 응답용으로 가리고, 사업자번호 원문이 실린 판정이 있으면 응답 한 번에 한 번 기록한다(B41, 화면 "문서 검증").
// 원문은 mask:bypass 가 있고 MCP 채널이 아닐 때만 실린다. 정보주체는 발주의 거래처다. 기록하지 못하면 판정을 내주지 않는다(fail-closed).
// s.DB 를 쓰므로 열린 rows 가 없을 때 불러야 한다.
func (s *Service) presentAll(ctx context.Context, p contract.Principal, vs []domain.Verdict) ([]domain.Verdict, error) {
	var subs []privacy.Subject
	seen := map[int64]bool{}
	if s.Privacy != nil && s.Masker != nil && contract.RawFieldAllowed(p) {
		for _, v := range vs {
			if !hasBusinessNo(v) {
				continue
			}
			var vendor sql.NullInt64
			if err := s.DB.QueryRowContext(ctx, `SELECT vendor_id FROM purchase_orders WHERE `+contract.SQLEntity(ctx, "purchase_orders")+` AND id = ?`, v.POID).Scan(&vendor); err != nil || !vendor.Valid || vendor.Int64 == 0 || seen[vendor.Int64] {
				continue
			}
			seen[vendor.Int64] = true
			subs = append(subs, privacy.Subject{Type: privacy.SubjectVendor, ID: vendor.Int64})
		}
	}
	if len(subs) > 0 {
		if err := s.Privacy.Note(ctx, p, privacy.Access{Category: privacy.CatAccount, Screen: privacy.ScreenVerifyDoc, Fields: []string{privacy.FieldBusinessNo}, Subjects: subs}); err != nil {
			log.Printf("docverify: 열람 기록 실패: %v", err)
			return nil, ErrPrivacyNote
		}
	}
	out := make([]domain.Verdict, len(vs))
	for i, v := range vs {
		out[i] = s.present(p, v)
	}
	return out, nil
}

func (s *Service) presentOne(ctx context.Context, p contract.Principal, v domain.Verdict) (domain.Verdict, error) {
	out, err := s.presentAll(ctx, p, []domain.Verdict{v})
	if err != nil {
		return domain.Verdict{}, err
	}
	return out[0], nil
}

// ErrPrivacyNote 는 열람 기록을 남기지 못해 원문을 보여 줄 수 없다는 뜻이다.
var ErrPrivacyNote = errors.New("열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요")

func hasBusinessNo(v domain.Verdict) bool {
	if v.Extraction.BusinessNo != "" {
		return true
	}
	for _, it := range v.Items {
		if it.Field == "business_no" && (it.Expected != "" || it.Actual != "") {
			return true
		}
	}
	return false
}

const verdictSelect = `SELECT v.id, v.document_id, v.po_id, COALESCE(v.receipt_id,0), v.outcome, v.items_json, v.created_at,
	COALESCE(v.confirmed_by,0), COALESCE(v.confirmed_outcome,''), COALESCE(e.extraction_json,'{}'), d.department_id, COALESCE(d.text_content,'')
	FROM document_verdicts v JOIN documents d ON d.id = v.document_id LEFT JOIN document_extractions e ON e.document_id = d.id`

type scanner interface{ Scan(dest ...any) error }

func scanVerdict(r scanner) (domain.Verdict, int64, error) {
	var v domain.Verdict
	var outcome, itemsJSON, created, confirmed, exJSON string
	var dept int64
	if err := r.Scan(&v.ID, &v.DocumentID, &v.POID, &v.ReceiptID, &outcome, &itemsJSON, &created, &v.ConfirmedBy, &confirmed, &exJSON, &dept, &v.SourceText); err != nil {
		return v, 0, err
	}
	v.Outcome = domain.Outcome(outcome)
	v.ConfirmedOutcome = domain.Outcome(confirmed)
	v.CreatedAt, _ = time.Parse(time.RFC3339, created)
	var raw []jsonItem
	_ = json.Unmarshal([]byte(itemsJSON), &raw)
	for _, it := range raw {
		v.Items = append(v.Items, domain.VerdictItem{Field: it.Field, Expected: it.Expected, Actual: it.Actual, PO: it.PO, Receipt: it.Receipt, Result: domain.Outcome(it.Result), Note: it.Note})
	}
	v.Extraction = extractionFromJSON(exJSON)
	return v, dept, nil
}

func visible(p contract.Principal, dept int64) bool {
	return p.Can(contract.PermPurchaseReadAll) || p.DepartmentID == dept
}

// GetVerdict 는 domain.DocumentService 를 구현한다. 타 부서 문서는 purchase:read_all 이 없으면 존재하지 않는 것처럼 답한다.
func (s *Service) GetVerdict(ctx context.Context, id int64) (domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentRead)
	if err != nil {
		return domain.Verdict{}, err
	}
	v, dept, err := scanVerdict(s.DB.QueryRowContext(ctx, verdictSelect+` WHERE v.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !visible(p, dept)) {
		return domain.Verdict{}, contract.ErrNotFound
	}
	if err != nil {
		return domain.Verdict{}, err
	}
	return s.presentOne(ctx, p, v)
}

// ListVerdicts 는 최근 판정 목록이다(domain.DocumentService 밖의 보조 조회). 부서 격리를 적용한다.
func (s *Service) ListVerdicts(ctx context.Context, limit int) ([]domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentRead)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := verdictSelect
	var args []any
	if !p.Can(contract.PermPurchaseReadAll) {
		q += ` WHERE d.department_id = ?`
		args = append(args, p.DepartmentID)
	}
	q += ` ORDER BY v.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []domain.Verdict
	for rows.Next() {
		v, _, err := scanVerdict(rows)
		if err != nil {
			return nil, err
		}
		v.SourceText = "" // 목록에는 원문을 싣지 않는다(상세에서만).
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	out, err := s.presentAll(ctx, p, list)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.Verdict{}
	}
	return out, nil
}

// Confirm 은 domain.DocumentService 를 구현한다. 재무(document:confirm)만 가능하고 한 번만 확정된다.
// AI 판정과 다르게 확정하면 사유(Comment)가 필요하다.
func (s *Service) Confirm(ctx context.Context, verdictID int64, in domain.ConfirmInput) (domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentConfirm)
	if err != nil {
		s.denied(ctx, p, "verdict.confirm", audit.TargetVerdict, idStr(verdictID))
		return domain.Verdict{}, err
	}
	switch in.Outcome {
	case domain.OutcomeMatch, domain.OutcomeMismatch, domain.OutcomeHold:
	default:
		return domain.Verdict{}, fmt.Errorf("%w: 확정 결과는 일치·불일치·보류 중 하나입니다", contract.ErrValidation)
	}
	var out domain.Verdict
	err = s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		v, dept, err := scanVerdict(tx.QueryRowContext(ctx, verdictSelect+` WHERE v.id = ?`, verdictID))
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !visible(p, dept) {
			return contract.ErrNotFound
		}
		if v.ConfirmedBy != 0 {
			return fmt.Errorf("%w: 이미 확정된 판정입니다", contract.ErrInvalidState)
		}
		if in.Outcome != v.Outcome && strings.TrimSpace(in.Comment) == "" {
			return fmt.Errorf("%w: AI 판정과 다르게 확정하려면 사유가 필요합니다", contract.ErrValidation)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE document_verdicts SET confirmed_by = ?, confirmed_outcome = ?, confirmed_at = ?, confirm_comment = ? WHERE id = ?`,
			p.UserID, string(in.Outcome), s.now().Format(time.RFC3339), strings.TrimSpace(in.Comment), verdictID); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "verdict.confirm", TargetType: audit.TargetVerdict, TargetID: idStr(verdictID), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "outcome", Old: string(v.Outcome), New: string(in.Outcome)}}}); err != nil {
			return err
		}
		v.ConfirmedBy, v.ConfirmedOutcome = p.UserID, in.Outcome
		// B31: 같은 확정 트랜잭션 안에서 자동 전표(또는 전표 대기 행)를 만든다. 금액·계정은 훅이 입고·발주 줄과 규칙 표에서만 정한다.
		// 전표 생성 실패는 훅이 대기 행으로 남기고 오류 없이 돌려주므로 확정은 막히지 않는다. 대기 행조차 쓰지 못한 오류만 확정 전체를 취소한다.
		// 훅 결과는 확정 응답에 싣지 않는다(응답 형식 불변).
		if s.JournalHook != nil {
			if _, herr := s.JournalHook.OnVerdictConfirmed(ctx, tx, v); herr != nil {
				return fmt.Errorf("전표를 처리하지 못해 확정하지 못했습니다. 잠시 뒤 다시 시도하세요: %w", herr)
			}
		}
		out = v
		return nil
	})
	if err != nil {
		return domain.Verdict{}, err
	}
	return s.presentOne(ctx, p, out)
}

// PendingConfirmations 는 아직 확정되지 않은 판정의 전체 건수와 최근 limit 건이다(홈 카드용, document:confirm).
// 목록과 같은 부서 격리 규칙을 따르고 원문은 싣지 않는다.
func (s *Service) PendingConfirmations(ctx context.Context, limit int) (int, []domain.Verdict, error) {
	p, err := contract.Authorize(ctx, contract.PermDocumentConfirm)
	if err != nil {
		return 0, nil, err
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	where := ` WHERE v.confirmed_by IS NULL`
	var args []any
	if !p.Can(contract.PermPurchaseReadAll) {
		where += ` AND d.department_id = ?`
		args = append(args, p.DepartmentID)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_verdicts v JOIN documents d ON d.id = v.document_id`+where, args...).Scan(&total); err != nil {
		return 0, nil, err
	}
	rows, err := s.DB.QueryContext(ctx, verdictSelect+where+` ORDER BY v.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var list []domain.Verdict
	for rows.Next() {
		v, _, err := scanVerdict(rows)
		if err != nil {
			return 0, nil, err
		}
		v.SourceText = ""
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	rows.Close()
	out, err := s.presentAll(ctx, p, list)
	if err != nil {
		return 0, nil, err
	}
	if out == nil {
		out = []domain.Verdict{}
	}
	return total, out, nil
}
