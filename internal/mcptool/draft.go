package mcptool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 초안 도구(B48 3단계, 명세 8절): 쓰기는 임시저장 초안 생성뿐이다. 상신·승인·확정·기표는 하지 않고, 결재선·승인자·상태·귀속 법인은 맵에 쓸 수 없다.
// 초안은 호출자 Principal 로 기존 서비스(구매요청 CreateDraft, 전표 Create)가 만든다: 권한·마감 월·검증·감사가 서비스 그대로다.

// DraftNote 는 초안 결과에 항상 붙는 안내다.
const DraftNote = "임시저장 초안만 만들었습니다. 확정(상신·승인)은 사람이 화면에서 하세요."

// MaxIdemKey 는 idempotency_key 의 최대 길이다(24시간 동안 같은 키는 같은 초안을 돌려준다).
const MaxIdemKey = 40

// DraftField 는 초안 대상의 칸이다. 맵에 쓸 수 있는 칸만 둔다(없는 칸은 선언해도 쓸 수 없다).
type DraftField struct {
	Name     string
	Label    string
	Type     string // string|int|date
	Required bool
}

// DraftTarget 은 쓰기 도구가 만들 수 있는 초안의 종류다.
type DraftTarget struct {
	Name   string
	Label  string
	Perm   contract.Permission
	Fields []DraftField
	// Create 는 맵에서 만든 값으로 임시저장 초안을 만든다(호출자 context 로 서비스를 부른다). 결과는 번호·상태 같은 값 없는 요약이다.
	Create func(ctx context.Context, v map[string]any) (map[string]any, error)
}

func (t *DraftTarget) field(name string) (DraftField, bool) {
	for _, f := range t.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return DraftField{}, false
}

// MapValue 는 맵의 값 하나다: 입력 이름(from) 또는 상수(value) 하나.
type MapValue struct {
	From  string          `json:"from,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// 맵에 쓸 수 없는 칸 이름(어떤 초안 대상에도 이 이름의 칸을 두지 않는다). 시험이 카탈로그에 이 이름이 없음을 고정한다.
var ForbiddenMapFields = []string{"status", "approver", "approvers", "approval_line", "steps", "entity", "entity_id", "requester_id", "created_by", "submit", "approve", "confirm", "post"}

// WithDrafts 는 카탈로그에 초안 대상을 더한다.
func (c *Catalog) WithDrafts(ts ...*DraftTarget) *Catalog {
	if c.drafts == nil {
		c.drafts = map[string]*DraftTarget{}
	}
	for _, t := range ts {
		c.drafts[t.Name] = t
	}
	return c
}

// Draft 는 초안 대상을 찾는다.
func (c *Catalog) Draft(name string) (*DraftTarget, bool) {
	if c == nil {
		return nil, false
	}
	t, ok := c.drafts[name]
	return t, ok
}

// Drafts 는 이름 순 초안 대상 목록이다.
func (c *Catalog) Drafts() []*DraftTarget {
	out := make([]*DraftTarget, 0, len(c.drafts))
	for _, t := range c.drafts {
		out = append(out, t)
	}
	sortDrafts(out)
	return out
}

func sortDrafts(ts []*DraftTarget) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j-1].Name > ts[j].Name; j-- {
			ts[j-1], ts[j] = ts[j], ts[j-1]
		}
	}
}

// validateDraft 는 초안 정의(target·map)를 검사한다.
func validateDraft(e *errList, d Definition, cat *Catalog) {
	if d.Resource != "" || len(d.Filters) > 0 || len(d.Fields) > 0 || d.Sort != nil {
		e.add("kind", "초안 도구에는 자원·거름 조건·보여 줄 칸·정렬을 쓸 수 없습니다")
	}
	t, ok := cat.Draft(d.Target)
	if !ok {
		e.add("target", "고를 수 없는 초안 종류입니다")
		return
	}
	validateInputs(e, d, nil)
	if _, bad := d.Input["idempotency_key"]; bad {
		e.add("input.idempotency_key", "idempotency_key 는 서버가 예약한 이름입니다(초안 도구는 이 인자를 자동으로 받습니다)")
	}
	for name := range d.Map {
		if _, ok := t.field(name); !ok {
			e.add("map."+name, "이 초안에는 이 칸을 쓸 수 없습니다(결재선·상태·승인·법인 칸은 쓸 수 없습니다)")
		}
	}
	for _, f := range t.Fields {
		mv, has := d.Map[f.Name]
		if !has {
			if f.Required {
				e.add("map."+f.Name, "필수 칸입니다: "+f.Label)
			}
			continue
		}
		p := "map." + f.Name
		hasVal, hasFrom := len(mv.Value) > 0, mv.From != ""
		if hasVal == hasFrom {
			e.add(p, "값은 상수(value)나 입력(from) 중 하나만 정해야 합니다")
			continue
		}
		if hasFrom {
			in, ok := d.Input[mv.From]
			switch {
			case !ok:
				e.add(p+".from", "정의에 없는 입력 이름입니다")
			case !draftCompatible(f.Type, in.Type):
				e.add(p+".from", "입력 종류가 이 칸과 맞지 않습니다")
			}
			continue
		}
		var cv any
		if json.Unmarshal(mv.Value, &cv) != nil {
			e.add(p+".value", "상수 값 형식이 올바르지 않습니다")
			continue
		}
		if s, isStr := cv.(string); isStr && strings.HasPrefix(s, "@") {
			e.add(p+".value", "초안 칸에는 호출자 맥락 값을 쓸 수 없습니다")
			continue
		}
		if _, msg := coerceDraftConst(f, cv); msg != "" {
			e.add(p+".value", msg)
		}
	}
}

func draftCompatible(fieldType, inType string) bool {
	switch fieldType {
	case TypeString:
		return inType == TypeString || inType == TypeEnum
	default:
		return inType == fieldType
	}
}

func coerceDraftConst(f DraftField, v any) (any, string) {
	switch f.Type {
	case TypeString:
		s, ok := v.(string)
		if !ok || len([]rune(s)) > 200 {
			return nil, "문자 값이어야 합니다(200자 이내)"
		}
		return s, ""
	case TypeInt:
		x, ok := v.(float64)
		if !ok || x != float64(int64(x)) {
			return nil, "정수여야 합니다"
		}
		return int64(x), ""
	case TypeDate:
		s, ok := v.(string)
		if !ok || !validDate(s) {
			return nil, "날짜는 YYYY-MM-DD 형식이어야 합니다"
		}
		return s, ""
	}
	return nil, "지원하지 않는 칸 종류입니다"
}

// DraftValues 는 바인딩된 입력과 맵에서 초안 칸 값을 만든다. 서비스를 부르지 않는다(시험 실행의 미리보기도 이것이다).
func DraftValues(d Definition, t *DraftTarget, bound map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range t.Fields {
		mv, has := d.Map[f.Name]
		if !has {
			continue
		}
		if mv.From != "" {
			if v, present := bound[mv.From]; present {
				out[f.Name] = v
			}
			continue
		}
		var cv any
		if err := json.Unmarshal(mv.Value, &cv); err != nil {
			return nil, fmt.Errorf("%w: 정의가 올바르지 않습니다", contract.ErrValidation)
		}
		c, msg := coerceDraftConst(f, cv)
		if msg != "" {
			return nil, fmt.Errorf("%w: 정의가 올바르지 않습니다", contract.ErrValidation)
		}
		out[f.Name] = c
	}
	for _, f := range t.Fields {
		if _, ok := out[f.Name]; f.Required && !ok {
			return nil, inputErr(f.Name)
		}
	}
	return out, nil
}

// ExecuteDraft 는 초안 도구를 호출자 context 로 실행한다: 입력 바인딩 → 칸 값 → 서비스의 임시저장 생성.
func ExecuteDraft(ctx context.Context, cat *Catalog, d Definition, in map[string]any) (map[string]any, error) {
	t, ok := cat.Draft(d.Target)
	if !ok || t.Create == nil {
		return nil, fmt.Errorf("%w: 이 초안 종류는 지금 쓸 수 없습니다", contract.ErrInvalidState)
	}
	bound, err := BindInput(d, in)
	if err != nil {
		return nil, err
	}
	v, err := DraftValues(d, t, bound)
	if err != nil {
		return nil, err
	}
	return t.Create(ctx, v)
}

// BuiltinDrafts 는 초안 대상 2종을 서비스에 연결한다(명세 8절).
func BuiltinDrafts(d deps.Deps) []*DraftTarget {
	return []*DraftTarget{
		{
			Name: "purchase_request_draft", Label: "구매요청 초안(품목 한 줄)", Perm: contract.PermPurchaseCreate,
			Fields: []DraftField{
				{Name: "vendor_id", Label: "거래처 번호", Type: TypeInt, Required: true},
				{Name: "title", Label: "제목", Type: TypeString, Required: true},
				{Name: "reason", Label: "사유", Type: TypeString},
				{Name: "item_id", Label: "품목 번호", Type: TypeInt, Required: true},
				{Name: "quantity", Label: "수량", Type: TypeInt, Required: true},
				{Name: "unit_price", Label: "단가(원)", Type: TypeInt, Required: true},
				{Name: "due_date", Label: "희망 납기", Type: TypeDate},
				{Name: "delivery_place", Label: "납품처", Type: TypeString},
			},
			Create: func(ctx context.Context, v map[string]any) (map[string]any, error) {
				if d.Purchase == nil {
					return nil, errNotWired("구매요청")
				}
				in := domain.DraftInput{VendorID: asInt(v["vendor_id"]), Title: asStr(v["title"]), Reason: asStr(v["reason"]), DueDate: asStr(v["due_date"]), DeliveryPlace: asStr(v["delivery_place"]),
					Lines: []domain.PRLineInput{{ItemID: asInt(v["item_id"]), Quantity: asInt(v["quantity"]), UnitPrice: asInt(v["unit_price"])}}}
				pr, err := d.Purchase.CreateDraft(ctx, in)
				if err != nil {
					return nil, err
				}
				return map[string]any{"no": pr.No, "status": string(pr.Status), "total": pr.TotalAmount}, nil
			},
		},
		{
			Name: "journal_draft", Label: "전표 초안(차변·대변 한 쌍)", Perm: contract.PermLedgerWrite,
			Fields: []DraftField{
				{Name: "posting_date", Label: "전기일", Type: TypeDate, Required: true},
				{Name: "memo", Label: "적요", Type: TypeString},
				{Name: "debit_account", Label: "차변 계정코드", Type: TypeString, Required: true},
				{Name: "credit_account", Label: "대변 계정코드", Type: TypeString, Required: true},
				{Name: "amount", Label: "금액(원)", Type: TypeInt, Required: true},
			},
			Create: func(ctx context.Context, v map[string]any) (map[string]any, error) {
				if d.Journals == nil {
					return nil, errNotWired("전표")
				}
				amt := asInt(v["amount"])
				j, err := d.Journals.Create(ctx, domain.JournalInput{Kind: domain.JournalGeneral, PostingDate: asStr(v["posting_date"]), TradeDate: asStr(v["posting_date"]), Memo: asStr(v["memo"]),
					Lines: []domain.JournalLineInput{{AccountCode: asStr(v["debit_account"]), Side: "D", Amount: amt}, {AccountCode: asStr(v["credit_account"]), Side: "C", Amount: amt}}})
				if err != nil {
					return nil, err
				}
				return map[string]any{"no": j.No, "status": j.Status, "total": j.TotalAmount}, nil
			},
		},
	}
}

func asInt(v any) int64 {
	n, _ := v.(int64)
	return n
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}
