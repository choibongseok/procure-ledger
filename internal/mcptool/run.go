package mcptool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Caller 는 호출자 맥락 값(@my_department 등)의 원천이다. 호출자 본인의 값뿐이다.
type Caller struct {
	UserID       int64
	DepartmentID int64
	Today        string // YYYY-MM-DD
}

// CallerOf 는 context 의 Principal 에서 호출자 맥락을 만든다.
func CallerOf(p contract.Principal, now time.Time) Caller {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	return Caller{UserID: p.UserID, DepartmentID: p.DepartmentID, Today: now.In(loc).Format("2006-01-02")}
}

func inputErr(name string) error {
	return fmt.Errorf("%w: 입력 값이 형식에 맞지 않습니다: %s", contract.ErrValidation, name)
}

// BindInput 은 호출 입력을 정의의 입력 선언에 맞춘다. 알 수 없는 입력·필수 누락·형식 오류는 거부한다(입력 값은 오류에 싣지 않는다).
func BindInput(d Definition, in map[string]any) (map[string]any, error) {
	for name := range in {
		if _, ok := d.Input[name]; !ok {
			return nil, inputErr(name)
		}
	}
	out := map[string]any{}
	for name, spec := range d.Input {
		v, present := in[name]
		if present && v != nil {
			c, msg := coerce(spec, v)
			if msg != "" {
				return nil, inputErr(name)
			}
			out[name] = c
			continue
		}
		if len(spec.Default) > 0 {
			c, msg := coerce(spec, rawToAny(spec.Default))
			if msg != "" {
				return nil, inputErr(name)
			}
			out[name] = c
			continue
		}
		if spec.Required {
			return nil, inputErr(name)
		}
	}
	return out, nil
}

// BuildQuery 는 정의·바인딩된 입력·호출자 맥락을 서비스에 넘길 타입 있는 질의로 바꾼다. 문자열 SQL 은 만들지 않는다.
func BuildQuery(d Definition, res *Resource, bound map[string]any, who Caller) (Query, error) {
	q := Query{Fields: d.Fields, Limit: res.DefaultLimit, Sort: res.DefaultSort, Dir: res.DefaultDir}
	if d.Sort != nil {
		q.Sort, q.Dir = d.Sort.Field, d.Sort.Dir
	}
	if v, ok := bound["limit"].(int64); ok && v > 0 {
		q.Limit = int(v)
	}
	if q.Limit > res.MaxLimit {
		q.Limit = res.MaxLimit
	}
	for _, f := range d.Filters {
		spec, ok := res.Field(f.Field)
		if !ok {
			return q, fmt.Errorf("%w: 정의가 카탈로그와 맞지 않습니다", contract.ErrValidation)
		}
		var val any
		if f.From != "" {
			v, present := bound[f.From]
			if !present {
				continue // 선택 입력이 비면 그 조건은 걸지 않는다
			}
			val = v
			if f.Op == OpIn {
				s, _ := v.(string)
				parts := splitList(s)
				if len(parts) == 0 || len(parts) > MaxInList {
					return q, inputErr(f.From)
				}
				val = parts
			}
		} else {
			var cv any
			if err := json.Unmarshal(f.Value, &cv); err != nil {
				return q, fmt.Errorf("%w: 정의가 올바르지 않습니다", contract.ErrValidation)
			}
			if s, isStr := cv.(string); isStr && strings.HasPrefix(s, "@") {
				switch s {
				case CtxMyDepartment:
					val = who.DepartmentID
				case CtxMyUser:
					val = who.UserID
				case CtxToday:
					val = who.Today
				default:
					return q, fmt.Errorf("%w: 정의가 올바르지 않습니다", contract.ErrValidation)
				}
			} else {
				c, msg := coerceFilterConst(spec, f.Op, cv)
				if msg != "" {
					return q, fmt.Errorf("%w: 정의가 올바르지 않습니다", contract.ErrValidation)
				}
				val = c
			}
		}
		q.Conds = append(q.Conds, Cond{Field: f.Field, Op: f.Op, Value: val})
	}
	return q, nil
}

// Result 는 실행 결과다. Rows 는 정의의 반환 칸만 담는다.
type Result struct {
	Fields []string         `json:"fields"`
	Rows   []map[string]any `json:"rows"`
	Count  int              `json:"count"`
}

// Execute 는 정의를 호출자 context 로 실행한다. 권한·법인·열람 범위는 어댑터가 부르는 서비스가 판정한다(여기서 대신 읽지 않는다).
// mask 가 있으면 결과의 모든 글에 적용한다(서비스가 이미 가린 값 위에 한 번 더).
func Execute(ctx context.Context, cat *Catalog, d Definition, in map[string]any, who Caller, mask func(string) string) (Result, error) {
	res, ok := cat.Get(d.Resource)
	if !ok || res.List == nil {
		return Result{}, fmt.Errorf("%w: 이 자원은 지금 쓸 수 없습니다", contract.ErrInvalidState)
	}
	bound, err := BindInput(d, in)
	if err != nil {
		return Result{}, err
	}
	q, err := BuildQuery(d, res, bound, who)
	if err != nil {
		return Result{}, err
	}
	rows, err := res.List(ctx, q)
	if err != nil {
		return Result{}, err
	}
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	out := Result{Fields: d.Fields, Rows: make([]map[string]any, 0, len(rows))}
	for _, row := range rows {
		pr := make(map[string]any, len(d.Fields))
		for _, name := range d.Fields {
			spec, _ := res.Field(name)
			v := row[name]
			if s, isStr := v.(string); isStr {
				if spec.Class == ClassFreeText {
					s = clipFreeText(s)
				}
				if mask != nil {
					s = mask(s)
				}
				v = s
			}
			pr[name] = v
		}
		out.Rows = append(out.Rows, pr)
	}
	out.Count = len(out.Rows)
	return out, nil
}

// clipFreeText 는 자유 글을 한 줄·200자로 줄인다(결과가 모델에게 지시하는 경로를 좁힌다, 명세 12절 2항).
func clipFreeText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
