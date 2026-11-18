package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// ApprovalDocService 는 구매 품의서 외 양식 문서(일반 기안서·지출 결의서·업무 협조전)의 기안·상신·조회다(B23).
// 문서는 purchase_requests 표에 들어가고, 결재·합의 처리는 ApprovalService 가 같은 엔진으로 한다.
type ApprovalDocService struct {
	Base
	Forms     repository.SQLForms
	Purchases repository.PurchaseRepository
	Approvals repository.ApprovalRepository
	Users     repository.SQLLines
	Limits    AttachLimits
}

var _ domain.ApprovalDocService = (*ApprovalDocService)(nil)

// NewApprovalDocService 는 SQLite 저장소로 양식 문서 서비스를 만든다.
func NewApprovalDocService(b Base) *ApprovalDocService {
	return &ApprovalDocService{Base: b, Purchases: repository.SQLPurchases{}, Approvals: repository.SQLApprovals{}, Limits: b.Attach.OrDefault()}
}

const maxDocTitleRunes = 200

func (s *ApprovalDocService) lineDeps() lineDeps { return sqlLineDeps{} }

// editableForm 은 호출자가 이 양식으로 새로 기안할 수 있는지(사용 중, 필요 권한, 사용 부서 제한)를 확인하고 최신 게시 버전을 돌려준다.
func (s *ApprovalDocService) editableForm(ctx context.Context, p contract.Principal, code string) (repository.FormRow, repository.VersionRow, error) {
	f, err := s.Forms.FormByCode(ctx, s.DB, code)
	if err != nil {
		return f, repository.VersionRow{}, err
	}
	if f.Kind == domain.FormKindPurchase {
		return f, repository.VersionRow{}, fmt.Errorf("%w: 구매 품의서는 구매요청 화면에서 작성합니다", contract.ErrValidation)
	}
	if !f.Active || !permitted(p, f) {
		return f, repository.VersionRow{}, contract.ErrNotFound
	}
	v, err := s.Forms.LatestPublished(ctx, s.DB, f.ID)
	if err != nil {
		return f, v, err
	}
	if ok, err := s.deptAllowed(ctx, p, v.Definition.DeptLimit); err != nil {
		return f, v, err
	} else if !ok {
		return f, v, fmt.Errorf("%w: 이 양식은 %s 소속만 쓸 수 있습니다.", contract.ErrValidation, s.limitNames(ctx, v.Definition.DeptLimit))
	}
	return f, v, nil
}

func amountOf(def domain.FormDefinition, fields map[string]any) int64 {
	if def.Settings.AmountField == "" {
		return 0
	}
	n, _ := asInt(fields[def.Settings.AmountField])
	return n
}

func marshalFields(fields map[string]any) (string, error) {
	if fields == nil {
		return "{}", nil
	}
	b, err := json.Marshal(fields)
	return string(b), err
}

func normalizeEdits(edits []domain.LineEdit) string {
	if len(edits) == 0 {
		return ""
	}
	b, _ := json.Marshal(edits)
	return string(b)
}

func parseEdits(raw string) []domain.LineEdit {
	if raw == "" {
		return nil
	}
	var e []domain.LineEdit
	_ = json.Unmarshal([]byte(raw), &e)
	return e
}

// jsonRoundTrip 는 호출자가 준 값(정수형 등)을 JSON 으로 풀린 값(float64·[]any)으로 맞춘다. 검증은 이 형태를 본다.
func jsonRoundTrip(fields map[string]any) (map[string]any, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// PreviewLine 은 양식 문서의 결재선을 만들어 보여 준다(저장하지 않는다). 상신이 쓰는 buildDocLine 과 같은 함수다.
func (s *ApprovalDocService) PreviewLine(ctx context.Context, in domain.DocLineInput) (domain.LinePreview, error) {
	pv, err := s.previewLine(ctx, in)
	if err == nil {
		s.annotateAbsence(ctx, pv.Steps)
	}
	return pv, err
}

func (s *ApprovalDocService) previewLine(ctx context.Context, in domain.DocLineInput) (domain.LinePreview, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		return domain.LinePreview{}, err
	}
	f, v, err := s.editableForm(ctx, p, in.FormCode)
	if err != nil {
		return domain.LinePreview{}, err
	}
	fields := in.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	if in.FieldUnit != 0 {
		for _, fd := range v.Definition.Fields {
			if fd.Type == domain.FieldUnit {
				fields = copyMap(fields)
				fields[fd.Key] = float64(in.FieldUnit)
			}
		}
	}
	fields, err = jsonRoundTrip(fields)
	if err != nil {
		return domain.LinePreview{}, validation("입력값을 해석할 수 없습니다")
	}
	amount := in.Amount
	if amount == 0 {
		amount = amountOf(v.Definition, fields)
	}
	asOf := contract.DayOf(s.now())
	dl, err := s.buildDocLine(ctx, s.DB, s.lineDeps(), docLineArgs{Form: f, Def: v.Definition, RequesterID: p.UserID, Fields: fields, Amount: amount, Edits: in.Edits, AsOf: asOf})
	if err != nil {
		return domain.LinePreview{}, err
	}
	dl.Preview.AsOf = asOf
	return dl.Preview, nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *ApprovalDocService) checkTitle(title string) (string, error) {
	t := strings.TrimSpace(title)
	if t == "" || utf8.RuneCountInString(t) > maxDocTitleRunes {
		return "", validation("제목은 1자 이상 %d자 이하여야 합니다", maxDocTitleRunes)
	}
	return t, nil
}

// CreateDraft 는 양식 문서를 임시저장으로 만든다(필수 항목은 상신 때 검사한다).
func (s *ApprovalDocService) CreateDraft(ctx context.Context, in domain.DocDraftInput) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval_doc.create", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	f, v, err := s.editableForm(ctx, p, in.FormCode)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if err := guardSystemForm(ctx, f.Code); err != nil {
		return domain.PurchaseRequest{}, err
	}
	title, err := s.checkTitle(in.Title)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	fields, err := jsonRoundTrip(in.Fields)
	if err != nil {
		return domain.PurchaseRequest{}, validation("입력값을 해석할 수 없습니다")
	}
	if err := ValidateFields(v.Definition.Fields, fields, false); err != nil {
		return domain.PurchaseRequest{}, err
	}
	if in.Urgent && !v.Definition.Settings.UrgentAllowed {
		return domain.PurchaseRequest{}, validation("이 양식은 긴급 표시를 쓸 수 없습니다")
	}
	fj, err := marshalFields(fields)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	ownerUnit, err := s.ownerUnitOf(ctx, p.UserID)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	var out repository.PRRecord
	err = s.write(ctx, p, "approval_doc.create", audit.TargetApprovalDoc, 0, func(tx contract.Tx) error {
		ru, err := s.Users.User(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		dept, err := (repository.SQLDelegations{}).User(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		_ = ru
		id, err := s.Purchases.InsertDoc(ctx, tx, repository.DocInsert{
			RequesterID: p.UserID, DepartmentID: dept.DepartmentID, OwnerUnitID: ownerUnit, FormID: f.ID, FormVersionID: v.ID,
			Title: title, FieldsJSON: fj, Amount: amountOf(v.Definition, fields), Urgent: in.Urgent, DeptVisible: v.Definition.Settings.DeptVisible, Now: s.now(),
		})
		if err != nil {
			return err
		}
		if err := s.Purchases.SetDraftEdits(ctx, tx, id, normalizeEdits(in.Edits)); err != nil {
			return err
		}
		if err := s.linkAttachments(ctx, tx, s.Limits, id, p.UserID, v.Definition.Fields, fields); err != nil {
			return err
		}
		if v.Definition.Numbering.AssignAt == "create" {
			if err := s.Purchases.SetDocNumber(ctx, tx, id, RenderNumber(v.Definition.Numbering.Pattern, NumberParts{FormCode: f.Code, Now: s.now(), ID: id})); err != nil {
				return err
			}
		}
		out, err = s.Purchases.GetPR(ctx, tx, id, repository.ScopeAll)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "approval_doc.create", audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "form_code", New: f.Code}, audit.DiffItem{Key: "form_version", New: fmt.Sprint(v.Version)},
			audit.DiffItem{Key: "status", New: string(domain.PRDraft)}, audit.DiffItem{Key: "amount", New: fmt.Sprint(out.Amount)},
			audit.DiffItem{Key: "urgent", New: fmt.Sprint(in.Urgent)})
	})
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	return s.present(ctx, p, out), nil
}

func (s *ApprovalDocService) ownRecord(ctx context.Context, tx contract.Tx, p contract.Principal, id int64) (repository.PRRecord, error) {
	rec, err := s.Purchases.GetPR(ctx, tx, id, repository.ScopeAll)
	if err != nil || rec.FormKind == string(domain.FormKindPurchase) || rec.RequesterID != p.UserID {
		if err == nil && rec.RequesterID != p.UserID && rec.FormKind != string(domain.FormKindPurchase) {
			// 남의 문서는 존재를 숨긴다(열람 범위 안이어도 수정은 본인만 하며, 범위 밖이면 404).
			return rec, contract.ErrNotFound
		}
		if err == nil {
			err = contract.ErrNotFound
		}
		return rec, err
	}
	return rec, nil
}

// UpdateDraft 는 임시저장·반려 상태 문서의 내용을 고친다(요청자 본인만).
func (s *ApprovalDocService) UpdateDraft(ctx context.Context, id int64, in domain.DocDraftInput) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval_doc.update", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	title, err := s.checkTitle(in.Title)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	fields, err := jsonRoundTrip(in.Fields)
	if err != nil {
		return domain.PurchaseRequest{}, validation("입력값을 해석할 수 없습니다")
	}
	ownerUnit, err := s.ownerUnitOf(ctx, p.UserID)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	var out repository.PRRecord
	err = s.write(ctx, p, "approval_doc.update", audit.TargetApprovalDoc, id, func(tx contract.Tx) error {
		rec, err := s.ownRecord(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if err := guardSystemForm(ctx, rec.FormCode); err != nil {
			return err
		}
		if !canEdit(rec.Status) {
			return fmt.Errorf("%w: %s 상태의 문서는 수정할 수 없습니다", contract.ErrInvalidState, StatusLabel(rec.Status))
		}
		ver, err := s.Forms.VersionByID(ctx, tx, rec.FormVersionID)
		if err != nil {
			return err
		}
		// 문서는 기안 때의 양식 버전으로 검증한다(새 버전이 나와도 바뀌지 않는다).
		if err := ValidateFields(ver.Definition.Fields, fields, false); err != nil {
			return err
		}
		if in.Urgent && !ver.Definition.Settings.UrgentAllowed {
			return validation("이 양식은 긴급 표시를 쓸 수 없습니다")
		}
		fj, err := marshalFields(fields)
		if err != nil {
			return err
		}
		amount := amountOf(ver.Definition, fields)
		if err := s.Purchases.UpdateDoc(ctx, tx, id, title, fj, amount, in.Urgent, s.now()); err != nil {
			return err
		}
		if err := s.Purchases.SetDraftEdits(ctx, tx, id, normalizeEdits(in.Edits)); err != nil {
			return err
		}
		if err := s.linkAttachments(ctx, tx, s.Limits, id, p.UserID, ver.Definition.Fields, fields); err != nil {
			return err
		}
		u, err := (repository.SQLDelegations{}).User(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if err := s.Purchases.SetOwner(ctx, tx, id, ownerUnit, u.DepartmentID); err != nil {
			return err
		}
		out, err = s.Purchases.GetPR(ctx, tx, id, repository.ScopeAll)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "approval_doc.update", audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "form_code", New: out.FormCode},
			audit.DiffItem{Key: "title", Old: rec.Title, New: title},
			audit.DiffItem{Key: "amount", Old: fmt.Sprint(rec.Amount), New: fmt.Sprint(amount)})
	})
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	return s.present(ctx, p, out), nil
}

// Submit 은 임시저장(또는 반려) 문서를 상신한다. 양식 규칙과 기안자 편집으로 결재선을 만들고, 번호를 매기고, 스냅샷을 저장한다.
func (s *ApprovalDocService) Submit(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval_doc.submit", audit.TargetApprovalDoc)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if pre, perr := s.Purchases.GetPR(ctx, s.DB, id, repository.ScopeAll); perr == nil && pre.RequesterID == p.UserID {
		if err := guardSystemForm(ctx, pre.FormCode); err != nil {
			return domain.PurchaseRequest{}, err
		}
	}
	var out repository.PRRecord
	for attempt := 0; ; attempt++ {
		out, err = s.submitOnce(ctx, p, id)
		if errors.Is(err, errOrgChanged) && attempt < 2 {
			continue
		}
		break
	}
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	pr := s.present(ctx, p, out)
	s.loadAuthority(ctx, s.DB, &pr)
	return pr, nil
}

func (s *ApprovalDocService) submitOnce(ctx context.Context, p contract.Principal, id int64) (repository.PRRecord, error) {
	preVer, err := repository.SQLLines{}.OrgVersion(ctx, s.DB)
	if err != nil {
		return repository.PRRecord{}, err
	}
	// 결재선은 조직 조회가 있어 트랜잭션 밖에서 만든다. 안에서는 내용이 그대로인지(OrgVersion·금액·편집)만 확인하고 스냅샷을 저장한다.
	var (
		dl       docLine
		def      domain.FormDefinition
		form     repository.FormRow
		ver      repository.VersionRow
		pre      repository.PRRecord
		preEdits string
		unitID   int64
		unitName string
		unitCode string
		havePre  bool
		preErr   error
	)
	asOf := contract.DayOf(s.now())
	if rec, perr := s.Purchases.GetPR(ctx, s.DB, id, repository.ScopeAll); perr == nil && rec.RequesterID == p.UserID && rec.FormKind != string(domain.FormKindPurchase) {
		pre, havePre = rec, true
		ver, preErr = s.Forms.VersionByID(ctx, s.DB, rec.FormVersionID)
		if preErr == nil {
			def = ver.Definition
			form, preErr = s.formByID(ctx, rec.FormID)
		}
		if preErr == nil {
			ok, derr := s.deptAllowed(ctx, p, def.DeptLimit)
			if derr != nil {
				return repository.PRRecord{}, derr
			}
			if !ok {
				return repository.PRRecord{}, fmt.Errorf("%w: 이 양식은 %s 소속만 쓸 수 있습니다.", contract.ErrValidation, s.limitNames(ctx, def.DeptLimit))
			}
			preEdits, preErr = s.Purchases.DraftEdits(ctx, s.DB, id)
		}
		if preErr == nil && ValidateFields(def.Fields, pre.Fields, true) == nil {
			dl, preErr = s.buildDocLine(ctx, s.DB, s.lineDeps(), docLineArgs{Form: form, Def: def, RequesterID: p.UserID, Fields: pre.Fields, Amount: pre.Amount, Edits: parseEdits(preEdits), AsOf: asOf})
			unitID, unitName, unitCode = s.draftingUnit(ctx, p.UserID)
		}
	}
	var out repository.PRRecord
	err = s.write(ctx, p, "approval_doc.submit", audit.TargetApprovalDoc, id, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("pr:%d", id)); err != nil { // 같은 문서의 상신·결재·취소·회수를 PG 에서 줄 세운다
			return err
		}
		rec, err := s.ownRecord(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if cur, err := (repository.SQLLines{}).OrgVersion(ctx, tx); err != nil {
			return err
		} else if cur != preVer {
			return errOrgChanged
		}
		ev, action, auditAction := evSubmit, domain.ActionSubmit, "approval_doc.submit"
		if rec.Status == domain.PRRejected {
			ev, action, auditAction = evResubmit, domain.ActionResubmit, "approval_doc.resubmit"
		}
		to, err := transition(rec.Status, ev)
		if err != nil {
			return err
		}
		if !havePre || preErr != nil || rec.Amount != pre.Amount || rec.Title != pre.Title {
			if preErr != nil && !errors.Is(preErr, contract.ErrNotFound) {
				return preErr
			}
			return fmt.Errorf("%w: 상신하는 동안 내용이 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)
		}
		if curEdits, err := s.Purchases.DraftEdits(ctx, tx, id); err != nil {
			return err
		} else if curEdits != preEdits {
			return fmt.Errorf("%w: 상신하는 동안 결재선이 바뀌었습니다. 다시 시도해 주세요", contract.ErrInvalidState)
		}
		// 전체 필드 검사(상신은 필수까지 본다).
		if err := ValidateFields(def.Fields, rec.Fields, true); err != nil {
			return err
		}
		if !dl.Preview.Complete {
			return lineUnavailable{problems: dl.Preview.Problems}
		}
		round := rec.Round
		if ev == evResubmit {
			round++
		}
		now := s.now()
		// 번호(상신 때 매기는 규칙이고 아직 임시 번호일 때만).
		if strings.HasPrefix(rec.No, "TMP-") && def.Numbering.AssignAt == "submit" {
			seq := 0
			if UsesSeq(def.Numbering.Pattern) {
				scope := int64(0)
				if UsesUnit(def.Numbering.Pattern) {
					scope = unitID
				}
				seq, err = s.Forms.NextSeq(ctx, tx, form.ID, scope, SeqYear(def.Numbering, now))
				if err != nil {
					return err
				}
			}
			no := RenderNumber(def.Numbering.Pattern, NumberParts{FormCode: form.Code, Unit: unitName, UnitCode: unitCode, Now: now, Seq: seq, ID: id})
			pre, perr := repository.EntityNoPrefix(ctx, tx, contract.WriteEntityID(ctx)) // 기본 법인이 아니면 법인 코드가 앞에 붙는다(법인마다 번호가 따로 올라간다)
			if perr != nil {
				return perr
			}
			no = pre + no
			if err := s.Purchases.SetDocNumber(ctx, tx, id, no); err != nil {
				return err
			}
		}
		ru, err := (repository.SQLDelegations{}).User(ctx, tx, rec.RequesterID)
		if err != nil {
			return err
		}
		if err := s.Purchases.SetOwner(ctx, tx, id, dl.Meta.UnitID, ru.DepartmentID); err != nil {
			return err
		}
		if err := s.Approvals.InsertLine(ctx, tx, id, round, repository.LineSnapshot{
			AsOfDay: string(asOf), RequesterUnitID: dl.Meta.UnitID, RequesterUnitPath: dl.Preview.UnitPath, RequesterRank: dl.Meta.Rank,
			NotesJSON: notesJSON(dl.Preview.Notes), Now: now, FormVersionID: rec.FormVersionID, EditsJSON: preEdits, AuthorityJSON: authorityJSON(dl.Authority),
		}, dl.Steps); err != nil {
			return err
		}
		if err := s.Approvals.InsertRecipients(ctx, tx, id, round, dl.Recipients); err != nil {
			return err
		}
		if err := s.Approvals.InsertHistory(ctx, tx, repository.HistoryInsert{PRID: id, Round: round, ActorID: p.UserID, Action: action, At: now}); err != nil {
			return err
		}
		if err := s.Purchases.SetPRState(ctx, tx, id, to, round, "", now); err != nil {
			return err
		}
		out, err = s.Purchases.GetPR(ctx, tx, id, repository.ScopeAll)
		if err != nil {
			return err
		}
		if err := s.notifySubmitted(ctx, tx, out, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, auditAction, audit.TargetApprovalDoc, id,
			audit.DiffItem{Key: "form_code", New: form.Code}, audit.DiffItem{Key: "form_version", New: fmt.Sprint(ver.Version)},
			audit.DiffItem{Key: "doc_no", New: out.No},
			audit.DiffItem{Key: "status", Old: string(rec.Status), New: string(to)},
			audit.DiffItem{Key: "round", Old: fmt.Sprint(rec.Round), New: fmt.Sprint(round)},
			audit.DiffItem{Key: "amount", New: fmt.Sprint(rec.Amount)},
			audit.DiffItem{Key: "line", New: docLineSummary(dl.Preview)},
			audit.DiffItem{Key: "recipients", New: recipientSummary(dl.Recipients)},
			audit.DiffItem{Key: "line_edits", New: editSummary(parseEdits(preEdits))},
			audit.DiffItem{Key: "urgent", New: fmt.Sprint(rec.Urgent)})
	})
	return out, err
}

func (s *ApprovalDocService) formByID(ctx context.Context, id int64) (repository.FormRow, error) {
	forms, err := s.Forms.Forms(ctx, s.DB)
	if err != nil {
		return repository.FormRow{}, err
	}
	for _, f := range forms {
		if f.ID == id {
			return f, nil
		}
	}
	return repository.FormRow{}, contract.ErrNotFound
}

func docLineSummary(pv domain.LinePreview) string {
	var parts []string
	for _, st := range pv.Steps {
		if st.Omitted {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d:%s/%s %s", st.Order, st.Role, st.Title, st.ApproverName))
	}
	return strings.Join(parts, " / ")
}

func recipientSummary(rs []domain.DocRecipient) string {
	var parts []string
	for _, r := range rs {
		if r.TargetType == "user" && r.Note != "" {
			continue // 조직 참조에서 펼친 구성원은 조직 한 줄로 요약한다
		}
		parts = append(parts, r.Kind+":"+r.Name)
	}
	return strings.Join(parts, ",")
}

func editSummary(es []domain.LineEdit) string {
	var parts []string
	for _, e := range es {
		parts = append(parts, fmt.Sprintf("%s:%s#%d", e.Role, e.TargetType, e.TargetID))
	}
	return strings.Join(parts, ",")
}

// present 는 응답에 나가는 문서를 만든다: 민감 필드 마스킹과 이름 표시.
func (s *ApprovalDocService) present(ctx context.Context, p contract.Principal, rec repository.PRRecord) domain.PurchaseRequest {
	pr := toPR(rec)
	one := []domain.PurchaseRequest{pr}
	s.markPRNames(ctx, one)
	pr = one[0]
	pr.Attachments = s.docAttachments(ctx, rec.ID) // 이름·크기만(내용은 나가지 않는다)
	if rec.RequesterID == p.UserID && (rec.Status == domain.PRDraft || rec.Status == domain.PRRejected) {
		if raw, err := s.Purchases.DraftEdits(ctx, s.DB, rec.ID); err == nil {
			pr.DraftEdits, pr.EditNotes = s.pruneEdits(ctx, parseEdits(raw))
		}
	}
	if rec.FormVersionID != 0 {
		if v, err := s.Forms.VersionByID(ctx, s.DB, rec.FormVersionID); err == nil {
			pr.FormFields, pr.FormBody = v.Definition.Fields, v.Definition.Body
			related := docRelated(p, rec)
			pr.Fields = maskSensitive(v.Definition.Fields, pr.Fields, related && p.Channel != contract.ChannelMCP)
		}
	}
	return pr
}

// docRelated 는 호출자가 문서의 당사자(기안자·결재선 처리자·참조 대상)인지다.
func docRelated(p contract.Principal, rec repository.PRRecord) bool {
	if rec.RequesterID == p.UserID {
		return true
	}
	for _, st := range rec.Steps {
		if st.AssigneeID == p.UserID || st.ApproverID == p.UserID {
			return true
		}
	}
	for _, r := range rec.Recipients {
		if r.TargetType == "user" && r.TargetID == p.UserID {
			return true
		}
	}
	return false
}

// maskSensitive 는 sensitive 필드 값을 가린다(앞 두 글자만 남긴다). full 이 참이면 그대로 둔다.
func maskSensitive(defs []domain.FieldDef, fields map[string]any, full bool) map[string]any {
	if full || len(fields) == 0 {
		return fields
	}
	out := copyMap(fields)
	for _, d := range defs {
		if !d.Sensitive {
			continue
		}
		if sv, ok := out[d.Key].(string); ok {
			out[d.Key] = maskKeepTwo(sv)
		} else if _, ok := out[d.Key]; ok {
			out[d.Key] = "(민감)"
		}
	}
	return out
}

func maskKeepTwo(s string) string {
	rs := []rune(s)
	if len(rs) <= 2 {
		return strings.Repeat("*", len(rs))
	}
	return string(rs[:2]) + strings.Repeat("*", len(rs)-2)
}

// Get 은 문서 한 건이다. 열람 범위 밖이거나 구매 품의서이면 찾을 수 없음이다.
func (s *ApprovalDocService) Get(ctx context.Context, id int64) (domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftRead)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	rec, err := s.Purchases.GetPR(ctx, s.DB, id, scope)
	if err != nil {
		return domain.PurchaseRequest{}, err
	}
	if rec.FormKind == string(domain.FormKindPurchase) {
		return domain.PurchaseRequest{}, contract.ErrNotFound
	}
	if err := s.auditPrivilegedView(ctx, p, scope, rec); err != nil {
		return domain.PurchaseRequest{}, err
	}
	pr := s.present(ctx, p, rec)
	if err := s.decorate(ctx, s.DB, &pr); err != nil {
		return domain.PurchaseRequest{}, err
	}
	s.loadAuthority(ctx, s.DB, &pr)
	s.markCancelable(ctx, s.DB, p, &pr)
	pr.Editable = requestEditable(p, pr.RequesterID, pr.Status)
	s.touchRead(ctx, p, id)
	return pr, nil
}

// auditPrivilegedView 는 document:read_all 로만 열 수 있는 문서(당사자·조직장 범위·부서 공개가 아님)를 열 때 감사 기록을 남긴다(D15).
func (s *ApprovalDocService) auditPrivilegedView(ctx context.Context, p contract.Principal, scope repository.ViewScope, rec repository.PRRecord) error {
	if !scope.AllDocs || docRelated(p, rec) {
		return nil
	}
	for _, u := range scope.UnitIDs {
		if u == rec.OwnerUnitID {
			return nil
		}
	}
	if rec.DeptVisible && rec.DepartmentID == p.DepartmentID {
		return nil
	}
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, "approval_doc.view_privileged", audit.TargetApprovalDoc, rec.ID,
			audit.DiffItem{Key: "doc_no", New: rec.No}, audit.DiffItem{Key: "reason", New: "document:read_all"})
	})
}

// List 는 양식 문서 목록이다(구매 품의서 제외, 열람 범위 적용). 필드 값은 목록에 싣지 않는다.
func (s *ApprovalDocService) List(ctx context.Context, f domain.DocFilter) ([]domain.PurchaseRequest, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftRead)
	if err != nil {
		return nil, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return nil, err
	}
	recs, err := s.Purchases.ListPRs(ctx, s.DB, repository.PRQuery{Filter: domain.PRFilter{Status: f.Status, RequesterID: f.RequesterID, AfterID: f.AfterID, Limit: f.Limit}, Scope: scope, Only: "docs", FormCode: f.FormCode})
	if err != nil {
		return nil, err
	}
	out := make([]domain.PurchaseRequest, len(recs))
	for i, r := range recs {
		pr := toPR(r)
		pr.Fields = nil
		out[i] = pr
	}
	s.markPRNames(ctx, out)
	return out, nil
}
