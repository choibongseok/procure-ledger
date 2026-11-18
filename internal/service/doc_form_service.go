package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// FormService 는 결재 양식 읽기(기안 화면·MCP)와 관리(admin)다(B23, 명세 3.2).
type FormService struct {
	Base
	Forms repository.SQLForms
}

// NewFormService 는 SQLite 저장소로 양식 서비스를 만든다.
func NewFormService(b Base) *FormService { return &FormService{Base: b} }

var _ domain.FormService = (*FormService)(nil)

var (
	fieldKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	bodyRefRe   = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
	retentionOK = map[int]bool{0: true, 1: true, 3: true, 5: true, 10: true, 30: true}
)

func summaryOf(f repository.FormRow, v repository.VersionRow) domain.FormSummary {
	return domain.FormSummary{ID: f.ID, Code: f.Code, Category: f.Category, Name: f.Name, Kind: f.Kind, Version: v.Version, Active: f.Active}
}

// deptAllowed 는 사용 부서 제한(조직 목록, 하위 조직 포함)을 기안자가 만족하는지다. 제한이 없으면 항상 참.
// 조직 조회는 트랜잭션 밖에서만 부른다.
func (b Base) deptAllowed(ctx context.Context, p contract.Principal, limit []int64) (bool, error) {
	if len(limit) == 0 {
		return true, nil
	}
	if b.Org == nil {
		return false, nil
	}
	day := contract.DayOf(b.now())
	person, err := b.Org.Person(ctx, p.UserID, day)
	switch {
	case errors.Is(err, contract.ErrNotInOrg), errors.Is(err, contract.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	allowed := map[int64]bool{}
	for _, id := range limit {
		allowed[id] = true
	}
	if allowed[person.Primary.Unit.ID] {
		return true, nil
	}
	anc, err := b.Org.Ancestors(ctx, person.Primary.Unit.ID, day)
	if err != nil {
		return false, err
	}
	for _, u := range anc {
		if allowed[u.ID] {
			return true, nil
		}
	}
	return false, nil
}

func permitted(p contract.Principal, f repository.FormRow) bool {
	return f.RequiredPermission == "" || p.Can(contract.Permission(f.RequiredPermission))
}

// ListUsable 은 호출자가 지금 새로 기안할 수 있는 양식(사용 중, 필요 권한, 사용 부서 제한 통과)이다.
func (s *FormService) ListUsable(ctx context.Context) ([]domain.FormSummary, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		// 구매 품의서만 쓰는 사용자(draft:create 없음)도 구매 품의서는 볼 수 있어야 하므로 아래에서 양식별 권한으로 다시 거른다.
		p2, perr := contract.Authorize(ctx, contract.PermPurchaseCreate)
		if perr != nil {
			return nil, err
		}
		p = p2
	}
	forms, err := s.Forms.Forms(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	out := []domain.FormSummary{}
	for _, f := range forms {
		if !f.Active || !permitted(p, f) || isSystemOnlyForm(f.Code) {
			continue // 시스템 전용 양식(재고 조정 전표)은 기안 시작 목록에 나오지 않는다(양식 관리 목록에는 남는다)
		}
		v, err := s.Forms.LatestPublished(ctx, s.DB, f.ID)
		if errors.Is(err, contract.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ok, err := s.deptAllowed(ctx, p, v.Definition.DeptLimit)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, summaryOf(f, v))
		}
	}
	return out, nil
}

// Get 은 양식과 최신 게시 버전의 정의다. 사용 중지·권한·부서 제한에 걸리면 찾을 수 없음이다.
func (s *FormService) Get(ctx context.Context, code string) (domain.FormDetail, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		p2, perr := contract.Authorize(ctx, contract.PermPurchaseCreate)
		if perr != nil {
			return domain.FormDetail{}, err
		}
		p = p2
	}
	f, err := s.Forms.FormByCode(ctx, s.DB, code)
	if err != nil {
		return domain.FormDetail{}, err
	}
	if !permitted(p, f) || isSystemOnlyForm(f.Code) {
		return domain.FormDetail{}, contract.ErrNotFound
	}
	v, err := s.Forms.LatestPublished(ctx, s.DB, f.ID)
	if err != nil {
		return domain.FormDetail{}, err
	}
	if ok, err := s.deptAllowed(ctx, p, v.Definition.DeptLimit); err != nil {
		return domain.FormDetail{}, err
	} else if !ok {
		return domain.FormDetail{}, fmt.Errorf("%w: 이 양식은 %s 소속만 쓸 수 있습니다.", contract.ErrValidation, s.limitNames(ctx, v.Definition.DeptLimit))
	}
	return domain.FormDetail{FormSummary: summaryOf(f, v), VersionID: v.ID, Definition: v.Definition}, nil
}

func (b Base) limitNames(ctx context.Context, ids []int64) string {
	var names []string
	for _, id := range ids {
		if b.Org == nil {
			break
		}
		if u, err := b.Org.Unit(ctx, id, contract.DayOf(b.now())); err == nil {
			names = append(names, u.Name)
		}
	}
	if len(names) == 0 {
		return "지정된 조직"
	}
	return strings.Join(names, "·")
}

// List 는 관리 목록이다(form:manage).
func (s *FormService) List(ctx context.Context) ([]domain.FormAdminRow, error) {
	if _, err := s.authorize(ctx, contract.PermFormManage, "approval_form.list", audit.TargetApprovalForm); err != nil {
		return nil, err
	}
	forms, err := s.Forms.Forms(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	out := make([]domain.FormAdminRow, 0, len(forms))
	for _, f := range forms {
		latest, st, pub, draft, err := s.Forms.VersionSummary(ctx, s.DB, f.ID)
		if err != nil {
			return nil, err
		}
		row := domain.FormAdminRow{LatestVersion: latest, LatestStatus: st, PublishedCount: pub, HasDraftVersion: draft, Searchable: f.Searchable}
		row.FormSummary = domain.FormSummary{ID: f.ID, Code: f.Code, Category: f.Category, Name: f.Name, Kind: f.Kind, Version: latest, Active: f.Active}
		out = append(out, row)
	}
	return out, nil
}

func toFormVersion(v repository.VersionRow) domain.FormVersion {
	return domain.FormVersion{ID: v.ID, FormID: v.FormID, FormCode: v.FormCode, Version: v.Version, Status: v.Status, Definition: v.Definition, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt}
}

// GetVersion 은 버전 한 개다(form:manage).
func (s *FormService) GetVersion(ctx context.Context, versionID int64) (domain.FormVersion, error) {
	if _, err := s.authorize(ctx, contract.PermFormManage, "approval_form.read", audit.TargetApprovalForm); err != nil {
		return domain.FormVersion{}, err
	}
	v, err := s.Forms.VersionByID(ctx, s.DB, versionID)
	return toFormVersion(v), err
}

// CreateVersionDraft 는 최신 게시 버전을 복사한 초안을 만든다. 이미 초안이 있으면 새로 만들지 않고 그 초안을 돌려준다.
func (s *FormService) CreateVersionDraft(ctx context.Context, code string) (domain.FormVersion, error) {
	p, err := s.authorize(ctx, contract.PermFormManage, "approval_form.create", audit.TargetApprovalForm)
	if err != nil {
		return domain.FormVersion{}, err
	}
	f, err := s.Forms.FormByCode(ctx, s.DB, code)
	if err != nil {
		return domain.FormVersion{}, err
	}
	var out repository.VersionRow
	err = s.write(ctx, p, "approval_form.create", audit.TargetApprovalForm, f.ID, func(tx contract.Tx) error {
		_, _, _, hasDraft, err := s.Forms.VersionSummary(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		if hasDraft {
			// 이미 작성 중인 새 버전이 있으면 그것을 돌려준다(새로 만들지 않는다).
			cur, err := s.Forms.LatestDraft(ctx, tx, f.ID)
			if err != nil {
				return err
			}
			out = cur
			return nil
		}
		base, err := s.Forms.LatestPublished(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		id, err := s.Forms.InsertDraft(ctx, tx, f.ID, base.Definition, s.now())
		if err != nil {
			return err
		}
		out, err = s.Forms.VersionByID(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "approval_form.create", audit.TargetApprovalForm, f.ID,
			audit.DiffItem{Key: "form_code", New: f.Code}, audit.DiffItem{Key: "version", New: fmt.Sprint(out.Version)})
	})
	return toFormVersion(out), err
}

// SaveVersionDraft 는 초안의 내용을 저장한다. 시스템 양식은 필드·본문을 바꿀 수 없다. 게시된 버전은 저장할 수 없다.
func (s *FormService) SaveVersionDraft(ctx context.Context, versionID int64, in domain.FormVersionInput) (domain.FormVersion, error) {
	p, err := s.authorize(ctx, contract.PermFormManage, "approval_form.draft_save", audit.TargetApprovalForm)
	if err != nil {
		return domain.FormVersion{}, err
	}
	var out repository.VersionRow
	err = s.write(ctx, p, "approval_form.draft_save", audit.TargetApprovalForm, versionID, func(tx contract.Tx) error {
		cur, err := s.Forms.VersionByID(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if cur.Status != "draft" {
			return fmt.Errorf("%w: 게시된 양식 버전은 수정할 수 없습니다. 새 버전을 만들어 고치세요", contract.ErrInvalidState)
		}
		def := in.Definition
		def.System = cur.Definition.System
		if cur.Definition.System {
			// 시스템 양식의 필드·본문은 코드가 정한다. 바꾸려는 시도는 조용히 무시하지 않고 거부한다.
			if fmt.Sprint(def.Fields) != fmt.Sprint(cur.Definition.Fields) || def.Body != cur.Definition.Body {
				return validation("시스템 양식의 필드와 본문은 고칠 수 없습니다. 번호 규칙·보존 연한·설정만 고칠 수 있습니다")
			}
		}
		if err := s.Forms.UpdateDraft(ctx, tx, versionID, def); err != nil {
			return err
		}
		out, err = s.Forms.VersionByID(ctx, tx, versionID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "approval_form.draft_save", audit.TargetApprovalForm, out.FormID,
			audit.DiffItem{Key: "form_code", New: out.FormCode}, audit.DiffItem{Key: "version", New: fmt.Sprint(out.Version)},
			audit.DiffItem{Key: "fields", New: fmt.Sprint(len(def.Fields))},
			audit.DiffItem{Key: "numbering", New: def.Numbering.Pattern},
			audit.DiffItem{Key: "changes", New: changedParts(cur.Definition, def)})
	})
	return toFormVersion(out), err
}

// changedParts 는 바뀐 항목 이름 목록이다(감사 diff, 내용은 넣지 않는다).
func changedParts(a, b domain.FormDefinition) string {
	var parts []string
	if fmt.Sprint(a.Fields) != fmt.Sprint(b.Fields) {
		parts = append(parts, "필드")
	}
	if a.Body != b.Body {
		parts = append(parts, "본문")
	}
	if fmt.Sprint(a.Line) != fmt.Sprint(b.Line) {
		parts = append(parts, "결재선")
	}
	if fmt.Sprint(a.Recipients) != fmt.Sprint(b.Recipients) {
		parts = append(parts, "수신·참조")
	}
	if a.Numbering != b.Numbering {
		parts = append(parts, "번호 규칙")
	}
	if a.RetentionYears != b.RetentionYears {
		parts = append(parts, "보존 연한")
	}
	if fmt.Sprint(a.DeptLimit) != fmt.Sprint(b.DeptLimit) {
		parts = append(parts, "사용 부서")
	}
	if a.Settings != b.Settings {
		parts = append(parts, "설정")
	}
	return strings.Join(parts, ",")
}

// ValidateDefinition 은 게시 전 검증이다(명세 3.2). 위반 문구 목록을 돌려준다. 조직 존재 확인은 unitExists 로 한다(nil 이면 건너뜀).
func ValidateDefinition(def domain.FormDefinition, kind domain.FormKind, unitExists func(id int64) bool) []string {
	var problems []string
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }
	keys := map[string]domain.FieldDef{}
	if len(def.Fields) > 60 {
		add("필드는 60개까지 둘 수 있습니다.")
	}
	for _, f := range def.Fields {
		if !fieldKeyRe.MatchString(f.Key) {
			add("필드 키 「%s」는 영문 소문자로 시작하는 영문 소문자·숫자·밑줄 40자 이내여야 합니다.", f.Key)
		}
		if _, dup := keys[f.Key]; dup {
			add("필드 키 「%s」가 중복됩니다.", f.Key)
		}
		keys[f.Key] = f
		if strings.TrimSpace(f.Label) == "" {
			add("필드 「%s」의 이름을 입력하세요.", f.Key)
		}
		problems = append(problems, validateFieldDef(f)...)
	}
	for _, f := range def.Fields {
		if f.SumOf == "" {
			continue
		}
		tk, ck, ok := strings.Cut(f.SumOf, ".")
		t, found := keys[tk]
		colOK := false
		if found && t.Type == domain.FieldTable {
			for _, c := range t.Columns {
				colOK = colOK || c.Key == ck && c.Type == domain.FieldAmount
			}
		}
		if f.Type != domain.FieldAmount || !ok || !colOK {
			add("금액 필드 「%s」의 합계 규칙은 표 필드의 금액 열을 가리켜야 합니다(예: items.amount).", f.Label)
		}
	}
	for _, m := range bodyRefRe.FindAllStringSubmatch(def.Body, -1) {
		if _, ok := keys[m[1]]; !ok {
			add("본문 서식의 {{%s}} 에 해당하는 필드가 없습니다.", m[1])
		}
	}
	problems = append(problems, ValidateNumbering(def.Numbering)...)
	if !retentionOK[def.RetentionYears] {
		add("보존 연한은 1·3·5·10·30년 또는 영구여야 합니다.")
	}
	st := def.Settings
	if st.LineEdit != "none" && st.LineEdit != "add_only" && st.LineEdit != "free" {
		add("기안자의 결재선 편집 범위가 올바르지 않습니다.")
	}
	if st.ObjectionAction != "record" && st.ObjectionAction != "reject" {
		add("합의 이견 처리 방식은 기록 후 진행 또는 반려여야 합니다.")
	}
	if st.AmountField != "" {
		if f, ok := keys[st.AmountField]; !ok || f.Type != domain.FieldAmount {
			add("결재선·전결 금액으로 쓸 필드 「%s」는 금액 필드여야 합니다.", st.AmountField)
		}
	}
	problems = append(problems, validateLineDef(def, keys, kind)...)
	if unitExists != nil {
		for _, id := range def.DeptLimit {
			if !unitExists(id) {
				add("사용 부서 제한의 조직(번호 %d)이 없습니다.", id)
			}
		}
	}
	if len(def.Recipients.CC) > 50 {
		add("참조처는 50곳까지 지정할 수 있습니다.")
	}
	if len(def.Recipients.Receive) > 10 {
		add("수신처는 10곳까지 지정할 수 있습니다.")
	}
	return problems
}

func validateFieldDef(f domain.FieldDef) []string {
	var p []string
	add := func(s string, a ...any) { p = append(p, fmt.Sprintf(s, a...)) }
	switch f.Type {
	case domain.FieldText, domain.FieldTextarea, domain.FieldNumber, domain.FieldAmount, domain.FieldDate, domain.FieldUnit:
	case domain.FieldSelect:
		if len(f.Options) < 1 || len(f.Options) > 50 {
			add("선택 필드 「%s」에는 선택지가 1~50개 있어야 합니다.", f.Label)
		}
	case domain.FieldTable:
		if len(f.Columns) < 1 || len(f.Columns) > 10 {
			add("표 필드 「%s」에는 열이 1~10개 있어야 합니다.", f.Label)
		}
		seen := map[string]bool{}
		for _, c := range f.Columns {
			if !fieldKeyRe.MatchString(c.Key) || seen[c.Key] {
				add("표 필드 「%s」의 열 키 「%s」가 올바르지 않거나 중복됩니다.", f.Label, c.Key)
			}
			seen[c.Key] = true
			switch c.Type {
			case domain.FieldText, domain.FieldNumber, domain.FieldAmount, domain.FieldDate:
			case domain.FieldSelect:
				if len(c.Options) == 0 {
					add("표 필드 「%s」의 선택 열 「%s」에는 선택지가 있어야 합니다.", f.Label, c.Label)
				}
			default:
				add("표 필드 「%s」의 열 「%s」 종류를 쓸 수 없습니다.", f.Label, c.Label)
			}
		}
		if f.MaxRows > 100 || f.MinRows > f.MaxRows && f.MaxRows != 0 {
			add("표 필드 「%s」의 행 수 설정이 올바르지 않습니다.", f.Label)
		}
	case domain.FieldAttachment:
		if f.MaxFiles > 20 {
			add("첨부 필드 「%s」는 20개까지 둘 수 있습니다.", f.Label)
		}
	default:
		add("필드 「%s」의 종류 「%s」를 알 수 없습니다.", f.Label, f.Type)
	}
	return p
}

func validateLineDef(def domain.FormDefinition, keys map[string]domain.FieldDef, kind domain.FormKind) []string {
	var p []string
	add := func(s string, a ...any) { p = append(p, fmt.Sprintf(s, a...)) }
	if def.Line.Rule != "" && def.Line.Rule != "amount_tiers" {
		add("기본 결재선 규칙 「%s」을 알 수 없습니다.", def.Line.Rule)
	}
	if def.Line.Rule == "amount_tiers" && def.Settings.AmountField == "" && kind != domain.FormKindPurchase {
		add("금액 구간 결재선을 쓰려면 금액 필드를 지정해야 합니다.")
	}
	if def.Line.Rule == "" && len(def.Line.Steps) == 0 {
		add("기본 결재선에 결재 단계가 하나 이상 있어야 합니다.")
	}
	if len(def.Line.Steps) > 12 {
		add("결재선 단계는 12개까지 둘 수 있습니다.")
	}
	parallel := map[int]int{}
	approvers := 0
	for i, st := range def.Line.Steps {
		switch st.Role {
		case "approve":
			approvers++
		case "agree":
			if st.Stage > 0 {
				parallel[st.Stage]++
			}
		case "cc", "receive":
		default:
			add("%d번째 단계의 역할 「%s」을 알 수 없습니다.", i+1, st.Role)
		}
		switch st.Source {
		case "org_level", "role_pool", "drafter_choice":
			if st.Source == "org_level" && st.Role == "approve" && st.Kind == "" {
				add("%d번째 단계의 직책을 정하세요.", i+1)
			}
		case "fixed_user":
			if st.UserID == 0 {
				add("%d번째 단계의 사용자를 지정하세요.", i+1)
			}
		case "unit_head":
			if st.UnitID == 0 && st.UnitCode == "" {
				add("%d번째 단계의 조직을 지정하세요.", i+1)
			}
		case "field_unit":
			if f, ok := keys[st.Field]; !ok || f.Type != domain.FieldUnit {
				add("%d번째 단계가 가리키는 필드 「%s」는 조직 선택 필드여야 합니다.", i+1, st.Field)
			}
		default:
			add("%d번째 단계의 결재자 지정 방식 「%s」을 알 수 없습니다.", i+1, st.Source)
		}
	}
	for stage, n := range parallel {
		if n > 5 {
			add("병렬 합의는 5명까지 지정할 수 있습니다(단계 %d).", stage)
		}
	}
	if def.Line.Rule == "" && len(def.Line.Steps) > 0 && approvers == 0 {
		add("기본 결재선에 결재 단계가 하나 이상 있어야 합니다.")
	}
	return p
}

// Publish 는 검증을 통과한 초안을 게시한다. 이전 게시 버전은 퇴역한다.
func (s *FormService) Publish(ctx context.Context, versionID int64) (domain.FormVersion, error) {
	p, err := s.authorize(ctx, contract.PermFormManage, "approval_form.publish", audit.TargetApprovalForm)
	if err != nil {
		return domain.FormVersion{}, err
	}
	cur, err := s.Forms.VersionByID(ctx, s.DB, versionID)
	if err != nil {
		return domain.FormVersion{}, err
	}
	f, err := s.Forms.FormByCode(ctx, s.DB, cur.FormCode)
	if err != nil {
		return domain.FormVersion{}, err
	}
	// 조직 존재 확인은 트랜잭션 밖에서 한다.
	exists := map[int64]bool{}
	for _, id := range cur.Definition.DeptLimit {
		if s.Org != nil {
			_, uerr := s.Org.Unit(ctx, id, contract.DayOf(s.now()))
			exists[id] = uerr == nil
		}
	}
	var unitExists func(int64) bool
	if s.Org != nil {
		unitExists = func(id int64) bool { return exists[id] }
	}
	var out repository.VersionRow
	err = s.write(ctx, p, "approval_form.publish", audit.TargetApprovalForm, f.ID, func(tx contract.Tx) error {
		v, err := s.Forms.VersionByID(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if v.Status != "draft" {
			return fmt.Errorf("%w: 작성 중인 버전만 게시할 수 있습니다", contract.ErrInvalidState)
		}
		if problems := ValidateDefinition(v.Definition, f.Kind, unitExists); len(problems) > 0 {
			return fmt.Errorf("%w: %s", contract.ErrValidation, strings.Join(problems, " / "))
		}
		if err := s.Forms.Publish(ctx, tx, f.ID, versionID, s.now()); err != nil {
			return err
		}
		out, err = s.Forms.VersionByID(ctx, tx, versionID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "approval_form.publish", audit.TargetApprovalForm, f.ID,
			audit.DiffItem{Key: "form_code", New: f.Code}, audit.DiffItem{Key: "version", Old: fmt.Sprint(out.Version - 1), New: fmt.Sprint(out.Version)},
			audit.DiffItem{Key: "fields", New: fmt.Sprint(len(out.Definition.Fields))},
			audit.DiffItem{Key: "numbering", New: out.Definition.Numbering.Pattern},
			audit.DiffItem{Key: "retention", New: fmt.Sprint(out.Definition.RetentionYears)},
			audit.DiffItem{Key: "dept_limit", New: fmt.Sprint(len(out.Definition.DeptLimit))})
	})
	return toFormVersion(out), err
}

// SetActive 는 양식을 사용 중지(새 기안 불가)하거나 다시 쓴다. 이미 기안한 문서는 그대로 진행된다.
func (s *FormService) SetActive(ctx context.Context, code string, active bool) error {
	action := "approval_form.retire"
	if active {
		action = "approval_form.activate"
	}
	p, err := s.authorize(ctx, contract.PermFormManage, action, audit.TargetApprovalForm)
	if err != nil {
		return err
	}
	f, err := s.Forms.FormByCode(ctx, s.DB, code)
	if err != nil {
		return err
	}
	return s.write(ctx, p, action, audit.TargetApprovalForm, f.ID, func(tx contract.Tx) error {
		if err := s.Forms.SetActive(ctx, tx, f.ID, active); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, audit.TargetApprovalForm, f.ID, audit.DiffItem{Key: "form_code", New: f.Code},
			audit.DiffItem{Key: "active", Old: fmt.Sprint(f.Active), New: fmt.Sprint(active)})
	})
}

// PreviewNumber 는 번호 규칙의 다음 번호 예다(호출자의 부서·지금 시각 기준, 일련번호는 올리지 않는다).
func (s *FormService) PreviewNumber(ctx context.Context, versionID int64) (string, error) {
	p, err := s.authorize(ctx, contract.PermFormManage, "approval_form.read", audit.TargetApprovalForm)
	if err != nil {
		return "", err
	}
	v, err := s.Forms.VersionByID(ctx, s.DB, versionID)
	if err != nil {
		return "", err
	}
	n := v.Definition.Numbering
	if problems := ValidateNumbering(n); len(problems) > 0 {
		return "", fmt.Errorf("%w: %s", contract.ErrValidation, strings.Join(problems, " "))
	}
	unitID, unitName, unitCode := s.draftingUnit(ctx, p.UserID)
	seq := 1
	if UsesSeq(n.Pattern) {
		scope := int64(0)
		if UsesUnit(n.Pattern) {
			scope = unitID
		}
		seq, err = s.Forms.PeekSeq(ctx, s.DB, v.FormID, scope, SeqYear(n, s.now()))
		if err != nil {
			return "", err
		}
	}
	return RenderNumber(n.Pattern, NumberParts{FormCode: v.FormCode, Unit: unitName, UnitCode: unitCode, Now: s.now(), Seq: seq, ID: 1}), nil
}

// draftingUnit 은 기안자의 "소속 부서" 조직(번호·이름·코드)이다: 주소속에서 위로 올라가며 처음 만나는 부서(department), 없으면 본부,
// 그것도 없으면 회사. 조직에 없으면 (0, "소속 없음", "").
func (b Base) draftingUnit(ctx context.Context, userID int64) (int64, string, string) {
	if b.Org == nil {
		return 0, "소속 없음", ""
	}
	day := contract.DayOf(b.now())
	person, err := b.Org.Person(ctx, userID, day)
	if err != nil {
		return 0, "소속 없음", ""
	}
	path, err := b.Org.Path(ctx, person.Primary.Unit.ID, day)
	if err != nil || len(path) == 0 {
		u := person.Primary.Unit
		return u.ID, u.Name, u.Code
	}
	pick := func(kind contract.OrgKind) (contract.OrgUnit, bool) {
		for i := len(path) - 1; i >= 0; i-- {
			if path[i].Kind == kind {
				return path[i], true
			}
		}
		return contract.OrgUnit{}, false
	}
	for _, k := range []contract.OrgKind{contract.OrgDepartment, contract.OrgDivision, contract.OrgCompany} {
		if u, ok := pick(k); ok {
			return u.ID, u.Name, u.Code
		}
	}
	u := path[len(path)-1]
	return u.ID, u.Name, u.Code
}
