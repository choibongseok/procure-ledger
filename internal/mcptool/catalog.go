// Package mcptool 은 관리자가 선언하는 사용자 정의 MCP 도구(B48)다: 정의 문법 검증, 조회 자원 카탈로그, 정의 저장·버전, 시험 실행.
// (권한·감사·한도), 어댑터는 erp·MCP 등록은 ai 몫이지만 B48 은 platform 이 함께 맡았다.
//
// 불변식
//   - 선언은 데이터다. 도구는 코드에 미리 만든 카탈로그에서 자원·거름 칸·반환 칸을 고를 뿐이고 임의 SQL·URL·코드는 없다.
//   - 호출자 권한·법인·마스킹은 서비스가 판정한다. 어댑터는 호출자 context 를 그대로 서비스에 넘기고(관리자 권한으로 대신 읽는 경로 없음),
//     선언의 거름은 서비스가 이미 적용한 결과를 더 좁힐 뿐이다.
//   - 쓰기와 감사는 같은 트랜잭션이다. 버전은 불변이다(DB 트리거).
package mcptool

import (
	"context"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 칸 종류.
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeDate   = "date"
	TypeBool   = "bool"
	TypeEnum   = "enum"
)

// 거름 연산(5종, D2). like·정규식·or·부정은 없다.
const (
	OpEq     = "eq"
	OpIn     = "in"
	OpGte    = "gte"
	OpLte    = "lte"
	OpPrefix = "prefix"
)

// 칸 분류. freetext 는 결과에서 200자 상한·줄바꿈 제거를 받는다(명세 12절 2항).
const (
	ClassPlain    = ""
	ClassFreeText = "freetext"
)

// 호출자 맥락 값(상수 자리에 쓸 수 있다). 호출자 본인의 범위를 좁히는 쪽으로만 쓰인다.
const (
	CtxMyDepartment = "@my_department"
	CtxMyUser       = "@my_user"
	CtxToday        = "@today"
)

// FieldSpec 은 자원의 한 칸이다. 거름·정렬·반환 가능 여부는 카탈로그가 정하고 선언은 그 안에서만 고른다.
type FieldSpec struct {
	Name     string
	Label    string // 화면에 보이는 한국어 이름
	Type     string
	Values   []string          // enum 값
	Labels   map[string]string // enum 값의 한국어 이름(화면과 결과 표가 쓴다)
	Ops      []string          // 거를 수 있는 연산(비면 거를 수 없는 칸)
	Sortable bool
	Return   bool
	Class    string
	Ctx      []string // 상수 자리에 쓸 수 있는 호출자 맥락 값
}

// Cond 는 검증을 지난 거름 조건 하나다. Value 는 string(문자열·날짜·enum)·int64·bool·[]string(in) 중 하나다.
type Cond struct {
	Field string
	Op    string
	Value any
}

// Query 는 어댑터가 받는 검증된 질의다.
type Query struct {
	Conds []Cond
	Sort  string
	Dir   string // asc | desc
	Limit int
	// Fields 는 돌려줄 칸(순서 유지). 어댑터는 모든 반환 가능 칸을 채워도 되고 실행기가 투영한다.
	Fields []string
}

// Resource 는 코드가 제공하는 조회 자원이다. List 는 호출자 context 로 서비스의 읽기 함수를 부르고 이미 가려진 값을 칸 이름 → 값으로 돌려준다.
type Resource struct {
	Name         string
	Label        string
	Perm         contract.Permission
	EntityOwned  bool
	DefaultLimit int
	MaxLimit     int
	Version      int // 카탈로그 칸·의미가 바뀌면 올린다(정의에 함께 저장해 불일치를 broken 으로 표시, 명세 18절)
	Fields       []FieldSpec
	DefaultSort  string
	DefaultDir   string
	List         func(ctx context.Context, q Query) ([]map[string]any, error)
}

// Field 는 이름으로 칸을 찾는다.
func (r *Resource) Field(name string) (FieldSpec, bool) {
	for _, f := range r.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// Catalog 는 자원 이름 → 자원 표다.
type Catalog struct {
	byName map[string]*Resource
	drafts map[string]*DraftTarget
}

// NewCatalog 는 카탈로그를 만든다.
func NewCatalog(rs ...*Resource) *Catalog {
	c := &Catalog{byName: map[string]*Resource{}}
	for _, r := range rs {
		c.byName[r.Name] = r
	}
	return c
}

// Get 은 자원을 찾는다.
func (c *Catalog) Get(name string) (*Resource, bool) {
	if c == nil {
		return nil, false
	}
	r, ok := c.byName[name]
	return r, ok
}

// All 은 이름 순 자원 목록이다.
func (c *Catalog) All() []*Resource {
	out := make([]*Resource, 0, len(c.byName))
	for _, r := range c.byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
