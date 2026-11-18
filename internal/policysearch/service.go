package policysearch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

const (
	// ActionSearch 는 감사 동작 이름이다(platform 의 auditLabels 와 같다).
	ActionSearch = "policy.search"

	defaultLimit  = 5
	maxLimit      = 20
	maxQueryRunes = 200
	maxItems      = 20
	maxItemRunes  = 100
	relatedTotal  = 5
)

// DeptNames 는 부서 ID 를 이름으로 바꾼다. 문서 머리말이 부서 이름을 쓰기 때문이다.
type DeptNames func(ctx context.Context, id int64) (string, error)

// Service 는 domain.PolicyService 를 구현한다.
//
// 열람 범위는 두 겹이다. 색인 검색이 볼 수 있는 조항만으로 질의하고(질의 조건), 결과를 돌려주기 전에 서비스가 문서 단위로 한 번 더 확인한다.
// 검색은 읽기지만 감사 로그에 남기며, 기록에 실패하면 결과를 돌려주지 않는다.
type Service struct {
	Txs    contract.TxManager
	Audit  audit.Recorder
	Masker contract.Masker
	Depts  DeptNames

	// Source 는 규정 폴더를 돌려준다. Reloadable 이면 검색할 때마다 내용이 바뀌었는지 보고 바뀌었으면 다시 색인한다.
	Source     func() fs.FS
	Reloadable bool

	// recheck 는 두 번째 열람 확인이다. 비어 있으면 rawAllows(머리말 원문에서 다시 계산)를 쓴다. 테스트만 바꾼다.
	recheck func(Doc, Viewer) bool

	mu  sync.RWMutex
	cur LoadResult
	ok  bool
}

var _ domain.PolicyService = (*Service)(nil)

// Init 는 서버 기동 시 색인을 만든다. 폴더를 읽을 수 없으면 오류(기동 실패), 문서 하나가 잘못된 것은 경고만 남기고 그 문서를 뺀다.
func (s *Service) Init() error {
	if s.Txs == nil || s.Audit == nil || s.Source == nil || s.Depts == nil {
		return errors.New("policysearch: Txs·Audit·Source·Depts 가 필요합니다")
	}
	_, err := s.reload()
	return err
}

func (s *Service) reload() (*Index, error) {
	res, err := Load(s.Source())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ok && s.cur.Fingerprint == res.Fingerprint {
		return s.cur.Index, nil
	}
	for _, p := range res.Problems {
		log.Printf("policysearch: 색인 제외: %s", p)
	}
	s.cur, s.ok = res, true
	return res.Index, nil
}

func (s *Service) index() (*Index, error) {
	if s.Reloadable {
		ix, err := s.reload()
		if err != nil {
			// 폴더가 잠시 읽히지 않으면 직전 색인을 계속 쓴다.
			s.mu.RLock()
			defer s.mu.RUnlock()
			if s.ok {
				log.Printf("policysearch: 재색인 실패, 직전 색인을 사용합니다: %v", err)
				return s.cur.Index, nil
			}
			return nil, err
		}
		return ix, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.ok {
		return nil, errors.New("policysearch: 색인이 만들어지지 않았습니다")
	}
	return s.cur.Index, nil
}

// Problems 는 마지막 색인에서 제외된 문서 목록이다(기동 로그·테스트용).
func (s *Service) Problems() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.cur.Problems...)
}

func (s *Service) record(ctx context.Context, e audit.Entry) error {
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Audit.Record(ctx, tx, e) })
}

// authorize 는 권한을 확인한다. 거부도 감사 로그에 남긴다(로그인한 사용자만. 기록 실패는 거부를 바꾸지 않는다).
func (s *Service) authorize(ctx context.Context) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, contract.PermPolicySearch)
	if err == nil {
		return p, nil
	}
	if p.UserID != 0 {
		if rerr := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetPolicy, TargetID: "0",
			Result: audit.ResultDenied}); rerr != nil {
			log.Printf("policysearch: 거부 감사 기록 실패: %v", rerr)
		}
	}
	return p, err
}

func (s *Service) viewer(ctx context.Context, p contract.Principal) (Viewer, error) {
	v := Viewer{Perms: ViewerPerms(p)} // 역할 이름이 아니라 실효 권한(사용자 정의 역할 포함)으로 판정한다
	if p.DepartmentID != 0 {
		name, err := s.Depts(ctx, p.DepartmentID)
		if err != nil && !errors.Is(err, contract.ErrNotFound) {
			return v, fmt.Errorf("부서 조회: %w", err)
		}
		v.Dept = name // 못 찾으면 비어 있고, 부서 전용 문서는 보이지 않는다(fail-closed)
	}
	return v, nil
}

func (s *Service) mask(text string) string {
	if s.Masker != nil {
		return s.Masker.MaskText(text)
	}
	return text
}

func clip(sv string, n int) string {
	if utf8.RuneCountInString(sv) <= n {
		return sv
	}
	return string([]rune(sv)[:n])
}

func toDomain(h Hit) domain.PolicyHit {
	return domain.PolicyHit{DocSlug: h.Doc.Slug, DocTitle: h.Doc.Title, ClauseNo: h.Clause.No, Heading: h.Clause.Heading,
		Text: h.Clause.Text, Source: h.Source(), Score: h.Score}
}

// rawAllows 는 열람 범위를 머리말 원문(RawRoles·RawDepts)에서 다시 계산한다. 색인의 Doc.Visible·parseScope 를 부르지 않는 독립 구현이다.
// 한쪽 구현이 틀려 문서를 열어 두더라도 다른 쪽이 막도록 일부러 따로 썼다. 형식이 이상하면 거부한다(fail-closed).
func rawAllows(d Doc, v Viewer) bool {
	perms, ok := v.perms()
	if !ok || d.RawRoles == "" || d.RawDepts == "" {
		return false
	}
	roleOK := false
	for _, part := range strings.Split(d.RawRoles, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "all" {
			roleOK = true
			break
		}
		if want, known := roleScopePerms[part]; known {
			for _, w := range want {
				roleOK = roleOK || perms.Has(w)
			}
		} else if strings.Contains(part, ":") {
			roleOK = roleOK || perms.Has(contract.Permission(part))
		}
	}
	deptOK := false
	for _, part := range strings.Split(d.RawDepts, ",") {
		part = strings.TrimSpace(part)
		if part == "all" || (v.Dept != "" && part == v.Dept) {
			deptOK = true
			break
		}
	}
	return roleOK && deptOK
}

// visibleOnly 는 두 번째 방어선이다. 색인이 돌려준 결과 중 호출자가 볼 수 없는 문서의 조항을 독립된 판정으로 한 번 더 걸러 낸다.
func (s *Service) visibleOnly(hits []Hit, v Viewer) []Hit {
	check := s.recheck
	if check == nil {
		check = rawAllows
	}
	out := hits[:0:0]
	for _, h := range hits {
		if check(h.Doc, v) {
			out = append(out, h)
		}
	}
	return out
}

// Search 는 domain.PolicyService 를 구현한다.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]domain.PolicyHit, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: 검색어가 비어 있습니다", contract.ErrValidation)
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return nil, fmt.Errorf("%w: 검색어는 %d자까지입니다", contract.ErrValidation, maxQueryRunes)
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	ix, err := s.index()
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, p)
	if err != nil {
		return nil, err
	}
	hits := s.visibleOnly(ix.Search(query, limit, v), v)
	if err := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetPolicy, TargetID: "search",
		Result: audit.ResultSuccess,
		Diff:   []audit.DiffItem{{Key: "query", New: s.mask(query)}, {Key: "count", New: strconv.Itoa(len(hits))}}}); err != nil {
		return nil, err
	}
	return convert(hits), nil
}

// ClauseBy 는 domain.PolicyService 를 구현한다: slug 와 조항 번호로 조항 한 건을 읽는다. 색인 쪽과 서비스 쪽 열람 판정을 모두 통과해야 하며,
// 범위 밖이거나 없는 조항은 같은 ErrNotFound 다. 읽기는 감사 로그(policy.search, 대상 clause)에 남는다.
func (s *Service) ClauseBy(ctx context.Context, slug, no string) (domain.PolicyHit, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return domain.PolicyHit{}, err
	}
	slug, no = strings.TrimSpace(slug), strings.TrimSpace(no)
	if slug == "" || no == "" {
		return domain.PolicyHit{}, fmt.Errorf("%w: 규정 문서와 조항 번호를 지정하세요", contract.ErrValidation)
	}
	if utf8.RuneCountInString(slug) > 100 || utf8.RuneCountInString(no) > 40 {
		return domain.PolicyHit{}, fmt.Errorf("%w: 해당 조항을 찾을 수 없습니다", contract.ErrNotFound)
	}
	ix, err := s.index()
	if err != nil {
		return domain.PolicyHit{}, err
	}
	v, err := s.viewer(ctx, p)
	if err != nil {
		return domain.PolicyHit{}, err
	}
	h, ok := ix.Find(slug, no, v)
	var seen []Hit
	if ok {
		seen = s.visibleOnly([]Hit{h}, v)
	}
	if err := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetPolicy, TargetID: "clause",
		Result: audit.ResultSuccess,
		Diff:   []audit.DiffItem{{Key: "query", New: s.mask(slug + " " + no)}, {Key: "count", New: strconv.Itoa(len(seen))}}}); err != nil {
		return domain.PolicyHit{}, err
	}
	if len(seen) == 0 {
		return domain.PolicyHit{}, fmt.Errorf("%w: 해당 조항을 찾을 수 없습니다", contract.ErrNotFound)
	}
	return toDomain(seen[0]), nil
}

func convert(hits []Hit) []domain.PolicyHit {
	out := make([]domain.PolicyHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, toDomain(h))
	}
	return out
}

// ApprovalTier 는 금액 구간과 그 구간의 결재선을 서술한 조항이다. Min 이상 Max 미만이고 Max 가 0 이면 상한이 없다.
// Steps 는 결재 단계 종류(팀장 team_lead, 부서장 dept_head, 본부장 division_head, 재무 finance) 순서다. 이 표는 migrations 의 approval_line_rules 시드와
// 같아야 하고 TestApprovalTiersMatchRuleSeedAndDocs 가 시드·규정 문서와 대조한다.
type ApprovalTier struct {
	Min, Max int64
	Steps    []string
	Doc      string
	Clause   string
}

// ApprovalTiers 는 구간 표다(복사본).
func ApprovalTiers() []ApprovalTier {
	return []ApprovalTier{
		{0, 1_000_000, []string{"team_lead"}, "purchase-approval-line", "제3조"},
		{1_000_000, 10_000_000, []string{"team_lead", "dept_head"}, "purchase-approval-line", "제4조"},
		{10_000_000, 100_000_000, []string{"team_lead", "dept_head", "finance"}, "purchase-approval-line", "제5조"},
		{100_000_000, 0, []string{"team_lead", "dept_head", "division_head", "finance"}, "purchase-approval-line", "제6조"},
	}
}

func tierFor(amount int64) ApprovalTier {
	ts := ApprovalTiers()
	for _, t := range ts {
		if amount >= t.Min && (t.Max == 0 || amount < t.Max) {
			return t
		}
	}
	return ts[len(ts)-1]
}

// 관련 조항의 묶음 표시(domain.PolicyHit.Reason).
const (
	ReasonTier = "tier" // 금액 구간 결재선 조항
	ReasonItem = "item" // 품목 이름이 규정 본문과 실제로 맞은 조항
)

// relatedItemMax 는 품목 관련 조항의 최대 건수다. 품목과 무관한 일반 조항이 구간 조항 옆에 줄지어 나오면 소음이라 적게 잡는다.
const relatedItemMax = 2

// Related 는 domain.PolicyService 를 구현한다. 금액이 속한 구간(ApprovalTiers)의 결재선 조항 1건(Reason tier)과,
// 품목 이름이 규정 본문과 실제로 맞은 조항 최대 2건(Reason item)을 돌려준다. 맞은 조항이 없으면 품목 조항은 0건이다.
func (s *Service) Related(ctx context.Context, in domain.PolicyRelatedInput) ([]domain.PolicyHit, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	if in.Amount < 0 {
		return nil, fmt.Errorf("%w: 금액이 음수입니다", contract.ErrValidation)
	}
	if len(in.Items) > maxItems {
		return nil, fmt.Errorf("%w: 품목은 %d개까지입니다", contract.ErrValidation, maxItems)
	}
	var names []string
	for _, it := range in.Items {
		if it = strings.TrimSpace(it); it != "" {
			names = append(names, clip(it, maxItemRunes))
		}
	}
	ix, err := s.index()
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, p)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	seen := map[string]bool{}
	var reasons []string
	add := func(hs []Hit, max int, reason string) {
		n := 0
		for _, h := range s.visibleOnly(hs, v) {
			k := h.Doc.Slug + "/" + h.Clause.No
			if seen[k] || n >= max || len(hits) >= relatedTotal {
				continue
			}
			seen[k] = true
			hits = append(hits, h)
			reasons = append(reasons, reason)
			n++
		}
	}
	t := tierFor(in.Amount)
	if h, ok := ix.Find(t.Doc, t.Clause, v); ok {
		add([]Hit{h}, 1, ReasonTier)
	}
	if len(names) > 0 {
		// 품목 이름 자체가 규정 본문과 맞을 때만 관련으로 본다(의미 있는 일치 하한 적용). 주제어를 덧붙여 일반 조항을 끌어오지 않는다.
		add(ix.search(strings.Join(names, " "), relatedTotal, v, true), relatedItemMax, ReasonItem)
	}
	if err := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetPolicy, TargetID: "related",
		Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{{Key: "amount", New: strconv.FormatInt(in.Amount, 10)},
			{Key: "items", New: s.mask(strings.Join(names, ", "))}, {Key: "count", New: strconv.Itoa(len(hits))}}}); err != nil {
		return nil, err
	}
	out := convert(hits)
	for i := range out {
		out[i].Reason = reasons[i]
	}
	return out, nil
}
