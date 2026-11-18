package contract

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// 다법인(B44, 9·10절).
//
// 법인 축은 세 가지 값으로 흐른다.
//   - EntitySet: 권한 하나가 효력을 갖는 법인 집합(그룹 범위면 All). Principal.EntityPerms 에 권한별로 실린다.
//   - 현재 법인(ActiveEntity): 요청이 일하는 법인. 인증 계층이 세션·헤더(X-Entity-ID)에서 정해 context 에 싣는다.
//   - EntityScope: 저장소가 SQL 조건으로 쓰는 읽기 범위(EntityAccess 가 위 둘에서 만든다).
//
// 법인이 하나뿐인 설치에서는 모든 값이 기본 법인(DefaultEntityID)이고 동작은 이전과 같다.

// DefaultEntityID 는 기본 법인의 번호다(이관이 기존 단일 회사를 이 번호로 옮긴다).
const DefaultEntityID int64 = 1

// 부여 범위(role_grants.scope).
const (
	ScopeOwn    = "own"    // 부여받은 사람의 소속·겸직 법인 전체(기본)
	ScopeEntity = "entity" // 지정한 법인 하나
	ScopeGroup  = "group"  // 모든 법인
)

// EntitySet 은 법인 집합이다. All 이면 모든 법인(그룹 범위)이다.
type EntitySet struct {
	All bool
	IDs []int64
}

// Has 는 법인이 집합에 있는지다.
func (s EntitySet) Has(id int64) bool {
	if s.All {
		return true
	}
	for _, x := range s.IDs {
		if x == id {
			return true
		}
	}
	return false
}

// Empty 는 어느 법인에서도 효력이 없는 집합인지다.
func (s EntitySet) Empty() bool { return !s.All && len(s.IDs) == 0 }

// Union 은 두 집합의 합집합이다(정렬·중복 제거).
func (s EntitySet) Union(o EntitySet) EntitySet {
	if s.All || o.All {
		return EntitySet{All: true}
	}
	return EntitySet{IDs: normIDs(append(append([]int64{}, s.IDs...), o.IDs...))}
}

// Intersects 는 두 집합이 겹치는 법인이 하나라도 있는지다(그룹은 모든 법인과 겹친다. 빈 집합은 겹치지 않는다).
func (s EntitySet) Intersects(o EntitySet) bool {
	switch {
	case s.Empty() || o.Empty():
		return false
	case s.All || o.All:
		return true
	}
	for _, x := range s.IDs {
		if o.Has(x) {
			return true
		}
	}
	return false
}

func normIDs(in []int64) []int64 {
	sort.Slice(in, func(i, j int) bool { return in[i] < in[j] })
	out := in[:0]
	for i, x := range in {
		if i == 0 || x != in[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// NewEntitySet 은 법인 번호 목록으로 집합을 만든다(정렬·중복 제거).
func NewEntitySet(ids ...int64) EntitySet { return EntitySet{IDs: normIDs(append([]int64{}, ids...))} }

// EntityScope 는 저장소가 SQL 에 붙이는 법인 읽기 범위다. All 이면 조건을 붙이지 않는다. 빈 IDs 는 아무 법인도 보지 못한다(전부 거부).
type EntityScope struct {
	All bool
	IDs []int64
}

// ScopeOf 는 집합을 읽기 범위로 바꾼다.
func (s EntitySet) ScopeOf() EntityScope { return EntityScope{All: s.All, IDs: s.IDs} }

// Cond 는 법인 열(alias.entity_id)에 붙일 조건 조각과 인자다. All 이면 항상 참이다.
func (s EntityScope) Cond(alias string) (string, []any) {
	if s.All {
		return "1 = 1", nil
	}
	if len(s.IDs) == 0 {
		return "1 = 0", nil
	}
	col := "entity_id"
	if alias != "" {
		col = alias + ".entity_id"
	}
	marks := make([]string, len(s.IDs))
	args := make([]any, len(s.IDs))
	for i, id := range s.IDs {
		marks[i], args[i] = "?", id
	}
	return fmt.Sprintf("%s IN (%s)", col, strings.Join(marks, ",")), args
}

// Has 는 법인이 읽기 범위 안인지다.
func (s EntityScope) Has(id int64) bool { return EntitySet(s).Has(id) }

// Single 은 범위가 법인 하나로 좁혀져 있으면 그 번호를 돌려준다(쓰기 대상 법인 결정에 쓴다).
func (s EntityScope) Single() (int64, bool) {
	if s.All || len(s.IDs) != 1 {
		return 0, false
	}
	return s.IDs[0], true
}

// ActiveEntity 는 요청이 일하는 현재 법인이다. All 이면 "전체" 조회 모드(그룹 범위가 있을 때만, 읽기 전용)다.
type ActiveEntity struct {
	ID  int64
	All bool
}

type activeEntityKey struct{}

// WithActiveEntity 는 context 에 현재 법인을 싣는다(인증 계층이 요청마다 정한다).
func WithActiveEntity(ctx context.Context, a ActiveEntity) context.Context {
	return context.WithValue(ctx, activeEntityKey{}, a)
}

// ActiveEntityFrom 은 context 의 현재 법인이다. 없으면 ok=false.
func ActiveEntityFrom(ctx context.Context) (ActiveEntity, bool) {
	a, ok := ctx.Value(activeEntityKey{}).(ActiveEntity)
	return a, ok
}

// EntityFrom 은 감사·기록 경로가 쓰는 현재 법인 번호다. 없으면 0(법인과 무관)이다. "전체" 모드도 0 이다(쓰기가 없다).
func EntityFrom(ctx context.Context) int64 {
	if a, ok := ActiveEntityFrom(ctx); ok && !a.All {
		return a.ID
	}
	return 0
}

// EntitiesFor 는 권한이 효력을 갖는 법인 집합이다. 계산되지 않은 Principal(시험·가짜 구현)은 권한이 있으면 전체 법인으로 본다.
// 설치에 법인이 하나뿐이면 이 값이 곧 기본 법인이라 동작이 같다.
func (p Principal) EntitiesFor(perm Permission) EntitySet {
	if !p.Can(perm) {
		return EntitySet{}
	}
	if p.EntityPerms == nil {
		return EntitySet{All: true}
	}
	if s, ok := p.EntityPerms[perm]; ok {
		return s
	}
	if perm == PermAuthenticated || perm == PermSetup || perm == PermPublic {
		return EntitySet{All: true}
	}
	return EntitySet{}
}

// SelectableEntities 는 이 사람이 현재 법인으로 고를 수 있는 법인이다: 소속·겸직 법인과 어떤 권한이든 효력이 있는 법인의 합집합(그룹 범위가 하나라도 있으면 All).
// 계산되지 않은 Principal(시험·가짜 구현)은 전체다.
func (p Principal) SelectableEntities() EntitySet {
	if p.EntityPerms == nil {
		return EntitySet{All: true}
	}
	set := NewEntitySet(p.OwnEntities...)
	for _, s := range p.EntityPerms {
		set = set.Union(s)
	}
	return set
}

// CanIn 은 그 법인에서 권한이 있는지다.
func (p Principal) CanIn(perm Permission, entityID int64) bool {
	return p.EntitiesFor(perm).Has(entityID)
}

// EntityAccess 는 법인 판정의 한 곳이다. 핸들러가 법인 if 분기를 만들지 않고 서비스·저장소가 이 함수를 쓴다.

// ErrEntityRequired 는 쓰기에 법인 하나가 정해져 있지 않을 때다("전체" 모드의 쓰기).
var ErrEntityRequired = fmt.Errorf("%w: 법인을 하나 골라야 합니다", ErrValidation)

// ReadScope 는 현재 요청이 권한 perm 으로 읽을 수 있는 법인 범위다. 인증된 Principal 이 없으면 ErrUnauthenticated, 권한이 없으면 ErrForbidden.
// 현재 법인이 정해져 있으면 그 법인(권한 범위 안일 때만), "전체" 모드면 권한 범위 전체(그룹 범위가 있어야 한다), 정해져 있지 않으면 권한 범위 전체다.
func ReadScope(ctx context.Context, perm Permission) (EntityScope, error) {
	p, err := Authorize(ctx, perm)
	if err != nil {
		return EntityScope{}, err
	}
	set := p.EntitiesFor(perm)
	a, has := ActiveEntityFrom(ctx)
	switch {
	case !has:
		return set.ScopeOf(), nil
	case a.All:
		if !set.All {
			return EntityScope{}, ErrForbidden
		}
		return EntityScope{All: true}, nil
	case !set.Has(a.ID):
		return EntityScope{}, ErrForbidden
	}
	return EntityScope{IDs: []int64{a.ID}}, nil
}

// WriteEntity 는 쓰기의 대상 법인이다. 현재 법인이 하나로 정해져 있어야 하고(전체 모드는 ErrEntityRequired) 그 법인에서 권한이 있어야 한다.
// 현재 법인이 정해지지 않은 context(서비스 내부 호출·시험)는 기본 법인으로 본다.
func WriteEntity(ctx context.Context, perm Permission) (int64, error) {
	p, err := Authorize(ctx, perm)
	if err != nil {
		return 0, err
	}
	id := DefaultEntityID
	if a, has := ActiveEntityFrom(ctx); has {
		if a.All {
			return 0, ErrEntityRequired
		}
		id = a.ID
	}
	if !p.CanIn(perm, id) {
		return 0, ErrForbidden
	}
	return id, nil
}

// RequireIn 은 대상 레코드의 법인에서 권한이 있는지 검사한다(레코드가 이미 있는 쓰기·상세 조회). 없으면 ErrNotFound(존재 여부를 알리지 않는다).
func RequireIn(ctx context.Context, perm Permission, entityID int64) error {
	p, err := Authorize(ctx, perm)
	if err != nil {
		return err
	}
	if !p.CanIn(perm, entityID) {
		if h, ok := ctx.Value(entityDeniedHookKey{}).(EntityDeniedHook); ok {
			h(ctx, perm, entityID) // 감사 access.denied 를 남긴다(응답은 그대로 404). 훅에 상대 법인 번호가 가지만 감사에는 적지 않는다.
		}
		return ErrNotFound
	}
	return nil
}

// EntityDeniedHook 은 다른 법인 레코드에 닿아 RequireIn 이 거부할 때 불린다(B44 AC-4411). 인증 계층이 요청마다 감사 기록기로 연결한다.
type EntityDeniedHook func(ctx context.Context, perm Permission, entityID int64)

type entityDeniedHookKey struct{}

// WithEntityDeniedHook 은 context 에 법인 불일치 감사 훅을 싣는다.
func WithEntityDeniedHook(ctx context.Context, h EntityDeniedHook) context.Context {
	return context.WithValue(ctx, entityDeniedHookKey{}, h)
}
