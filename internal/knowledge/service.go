package knowledge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/textindex"
)

const (
	// ActionSearch 는 감사 동작 이름이다(auditLabels 와 같다).
	ActionSearch = "knowledge.search"

	defaultLimit   = 5
	maxLimit       = 20
	maxOffset      = 200
	maxQueryRunes  = 200
	minQueryRunes  = 2
	candidateBatch = 200 // 한 번에 열람 판정하는 후보 조각 수(명세 6절 4번)
	candidateMax   = 1000
	snippetRunes   = 200
)

// Opener 는 결재 문서 열람 판정의 입구다(구현: service.DocViewService). 검색 서비스는 관계 판정(결재선·참조·부서 비교)을 직접 하지 않는다.
type Opener interface {
	Openable(ctx context.Context, ids []int64) (map[int64]bool, error)
	RelationOpenable(ctx context.Context, ids []int64) (map[int64]bool, error)
	Recheck(ctx context.Context, id int64) (bool, error)
}

// Service 는 domain.KnowledgeService 의 구현이다. 웹 핸들러와 MCP 도구가 같은 서비스를 쓴다.
type Service struct {
	Txs    contract.TxManager
	DB     contract.Tx
	Audit  audit.Recorder
	Masker contract.Masker
	Opener Opener
	Store  *Store
	// Privacy 는 개인정보 열람 기록(B41)이다. 업로드 원문·원본에 이메일·전화·계좌 패턴이 있으면 응답 전에 기록하고, 기록하지 못하면 응답하지 않는다(fail-closed).
	// nil 이면 개인정보가 있는 문서는 열 수 없다(운영 조립은 항상 채운다).
	Privacy privacy.Noter

	// recheck 는 두 번째 열람 확인이다. 비어 있으면 Opener.Recheck 를 쓴다. 시험이 일부러 어긋나게 만들 때만 바꾼다(AC-4710).
	recheck func(ctx context.Context, id int64) (bool, error)
	repo    repository.SQLKnowledge
}

var _ domain.KnowledgeService = (*Service)(nil)

func (s *Service) record(ctx context.Context, e audit.Entry) error {
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Audit.Record(ctx, tx, e) })
}

// authorize 는 권한을 확인한다. 거부도 감사에 남긴다(로그인한 사용자만, 기록 실패는 거부를 바꾸지 않는다).
func (s *Service) authorize(ctx context.Context) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, contract.PermKnowledgeSearch)
	if err == nil {
		return p, nil
	}
	if p.UserID != 0 {
		if rerr := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetKnowledge, TargetID: "0", Result: audit.ResultDenied}); rerr != nil {
			log.Printf("knowledge: 거부 감사 기록 실패: %v", rerr)
		}
	}
	return p, err
}

func (s *Service) mask(text string) string {
	if s.Masker != nil {
		return s.Masker.MaskText(text)
	}
	return text
}

func validation(msg string) error { return fmt.Errorf("%w: %s", contract.ErrValidation, msg) }

// Search 는 domain.KnowledgeService 를 구현한다(명세 6절 질의 흐름).
//
//  1. 현재 법인으로 조각 후보를 점수 순으로 뽑고(색인 질의 조건은 법인뿐),
//  2. 후보를 문서 단위로 묶어 결재 문서 열람 판정(Opener.Openable, ViewScope 한 번의 SQL)으로 거르고(200 개씩, 부족하면 다음 후보),
//  3. 돌려줄 결과마다 열람을 한 번 더 확인한다(Recheck).
//
// 결과에는 점수·전체 건수·"가려진 결과" 표시가 없다. 감사 기록에 실패하면 결과를 주지 않는다.
func (s *Service) Search(ctx context.Context, f domain.KnowledgeFilter) (domain.KnowledgeResult, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return domain.KnowledgeResult{}, err
	}
	query := strings.TrimSpace(f.Query)
	switch n := utf8.RuneCountInString(query); {
	case n == 0:
		return domain.KnowledgeResult{}, validation("검색어가 비어 있습니다")
	case n < minQueryRunes:
		return domain.KnowledgeResult{}, validation("내용 검색은 2자 이상 입력하세요")
	case n > maxQueryRunes:
		return domain.KnowledgeResult{}, validation(fmt.Sprintf("검색어는 %d자까지입니다", maxQueryRunes))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := f.Offset
	if offset < 0 || offset > maxOffset {
		return domain.KnowledgeResult{}, validation("더 볼 수 있는 범위를 넘었습니다")
	}
	kinds := map[string]bool{}
	for _, k := range f.Kinds {
		if _, ok := domain.KnowledgeKindLabels[k]; !ok {
			return domain.KnowledgeResult{}, validation("알 수 없는 종류입니다. 결재 문서·첨부·매뉴얼·규정 중에서 고르세요")
		}
		kinds[k] = true
	}
	snap, err := s.Store.Refresh(ctx)
	if err != nil {
		return domain.KnowledgeResult{}, err
	}
	ent, scoped := entityScope(ctx)
	viewer, err := s.viewer(ctx, p)
	if err != nil {
		return domain.KnowledgeResult{}, err
	}
	visible := func(chunkID int64) bool {
		r, ok := snap.rows[chunkID]
		if !ok {
			return false
		}
		if len(kinds) > 0 && !kinds[kindOfRow(r)] {
			return false
		}
		if scoped && r.EntityID != ent && r.EntityID != 0 {
			return false
		}
		if r.Kind == repository.KSrcUpload { // 업로드는 열람 범위(역할 AND 부서)가 색인 질의 조건이다
			d, ok := snap.scopes[r.SourceID]
			return ok && d.Visible(viewer)
		}
		return true
	}
	hits := snap.ix.Search(query, candidateMax, visible)

	type accepted struct {
		row repository.ChunkRow
	}
	var keep []accepted
	seenDoc := map[int64]bool{}
	openCache := map[int64]bool{}
	need := offset + limit + 1
	for start := 0; start < len(hits) && len(keep) < need; start += candidateBatch {
		end := start + candidateBatch
		if end > len(hits) {
			end = len(hits)
		}
		// 이 묶음에서 아직 판정하지 않은 결재 문서 번호를 모은다.
		var ids []int64
		pending := map[int64]bool{}
		for _, h := range hits[start:end] {
			r := snap.rows[h.ID]
			if id := openDocID(r); id > 0 {
				if _, done := openCache[id]; !done && !pending[id] {
					pending[id] = true
					ids = append(ids, id)
				}
			}
		}
		if len(ids) > 0 {
			ok, err := s.Opener.Openable(ctx, ids)
			if err != nil {
				return domain.KnowledgeResult{}, err
			}
			for _, id := range ids {
				openCache[id] = ok[id]
			}
		}
		for _, h := range hits[start:end] {
			r := snap.rows[h.ID]
			if seenDoc[r.DocID] {
				continue
			}
			if r.Kind != repository.KSrcUpload {
				id := openDocID(r)
				if id <= 0 || !openCache[id] {
					continue
				}
			}
			seenDoc[r.DocID] = true
			keep = append(keep, accepted{row: r})
			if len(keep) >= need {
				break
			}
		}
	}
	more := len(keep) > offset+limit
	if offset > len(keep) {
		offset = len(keep)
	}
	end := offset + limit
	if end > len(keep) {
		end = len(keep)
	}
	page := keep[offset:end]

	// 두 번째 열람 확인(독립 경로). 어긋나는 결과는 뺀다.
	recheck := s.recheck
	if recheck == nil {
		recheck = s.Opener.Recheck
	}
	out := domain.KnowledgeResult{More: more}
	var shownIDs []int64
	for _, a := range page {
		if a.row.Kind == repository.KSrcUpload {
			// 업로드: DB 의 업로드 행(역할·부서·법인·삭제 여부)에서 열람 범위를 독립으로 다시 계산한다.
			okRe, err := s.uploadAllowed(ctx, a.row.SourceID, viewer, ent, scoped)
			if err != nil {
				return domain.KnowledgeResult{}, err
			}
			if !okRe {
				continue
			}
			out.Hits = append(out.Hits, s.hit(a.row, fmt.Sprintf("/knowledge/%d", a.row.SourceID), query))
			continue
		}
		id := openDocID(a.row)
		okRe, err := recheck(ctx, id)
		if err != nil {
			return domain.KnowledgeResult{}, err
		}
		if !okRe {
			continue
		}
		link, _, lerr := s.repo.DocLink(ctx, s.DB, id)
		if lerr != nil {
			link = fmt.Sprintf("/docs/%d", id) // 재확인을 통과한 문서다. 링크 정보만 못 읽었으면 기본 경로(열 때 열람 판정이 다시 돈다)
		}
		out.Hits = append(out.Hits, s.hit(a.row, link, query))
		shownIDs = append(shownIDs, id)
	}
	// 특권 열람자: 관계 없이 특권으로만 열린 결과의 건수를 남긴다(명세 6절 7번).
	privileged := 0
	if len(shownIDs) > 0 && (p.Can(contract.PermDocumentReadAll) || p.Can(contract.PermPurchaseReadAll)) {
		rel, err := s.Opener.RelationOpenable(ctx, shownIDs)
		if err != nil {
			return domain.KnowledgeResult{}, err
		}
		for _, id := range shownIDs {
			if !rel[id] {
				privileged++
			}
		}
	}
	diff := []audit.DiffItem{{Key: "query", New: s.mask(query)}, {Key: "count", New: strconv.Itoa(len(out.Hits))}, {Key: "privileged_count", New: strconv.Itoa(privileged)},
		{Key: "channel", New: string(p.Channel)}}
	if scoped {
		diff = append(diff, audit.DiffItem{Key: "entity", New: strconv.FormatInt(ent, 10)})
	}
	if err := s.record(ctx, audit.Entry{Actor: p, Action: ActionSearch, TargetType: audit.TargetKnowledge, TargetID: "search", Result: audit.ResultSuccess, Diff: diff}); err != nil {
		return domain.KnowledgeResult{}, err
	}
	return out, nil
}

// entityScope 는 요청의 현재 법인이다(전체 모드·현재 법인 없음은 거르지 않는다).
func entityScope(ctx context.Context) (int64, bool) {
	if a, ok := contract.ActiveEntityFrom(ctx); ok && !a.All && a.ID > 0 {
		return a.ID, true
	}
	return 0, false
}

func kindOfRow(r repository.ChunkRow) string {
	switch r.Kind {
	case repository.KSrcApproval:
		return domain.KnowledgeApprovalDoc
	case repository.KSrcAttachment:
		return domain.KnowledgeAttachment
	}
	if r.UploadKind == "regulation" {
		return domain.KnowledgeRegulation
	}
	return domain.KnowledgeManual
}

// openDocID 는 열람 판정의 기준이 되는 결재 문서 번호다(첨부는 부모). 업로드는 0(결재 문서 판정이 아니다).
func openDocID(r repository.ChunkRow) int64 {
	switch r.Kind {
	case repository.KSrcApproval:
		return r.SourceID
	case repository.KSrcAttachment:
		return r.ParentDocID
	}
	return 0
}

func (s *Service) hit(r repository.ChunkRow, link, query string) domain.KnowledgeHit {
	h := domain.KnowledgeHit{Kind: kindOfRow(r), Title: r.Title, DocNo: r.DocNo, LocatorKind: r.LocatorKind, Locator: r.Locator, Heading: r.Heading,
		Snippet: s.mask(snippetOf(r.Text, query, snippetRunes)), Link: link}
	if r.Kind == repository.KSrcUpload {
		h.Version = r.Version
	}
	h.Source = SourceLine(h)
	return h
}

// SourceLine 은 출처 한 줄이다(명세 8절): "결재 문서 AB-2026-0012 「제목」 · 본문", 첨부는 "· 파일.pdf 3쪽".
func SourceLine(h domain.KnowledgeHit) string {
	label := domain.KnowledgeKindLabels[h.Kind]
	var b strings.Builder
	switch h.Kind {
	case domain.KnowledgeApprovalDoc:
		fmt.Fprintf(&b, "%s %s 「%s」 · 본문", label, h.DocNo, h.Title)
	case domain.KnowledgeAttachment:
		fmt.Fprintf(&b, "결재 문서 %s · %s", h.DocNo, h.Title)
		b.WriteString(locatorText(h))
	default:
		fmt.Fprintf(&b, "사내 %s 「%s」", label, h.Title)
		if h.Version > 1 {
			fmt.Fprintf(&b, " v%d", h.Version)
		}
		b.WriteString(locatorText(h))
	}
	return b.String()
}

func locatorText(h domain.KnowledgeHit) string {
	switch h.LocatorKind {
	case "page":
		return " " + h.Locator + "쪽"
	case "clause":
		if h.Heading != "" {
			return " " + h.Locator + " (" + h.Heading + ")"
		}
		return " " + h.Locator
	case "heading":
		return " 「" + h.Locator + "」"
	case "para":
		return " " + h.Locator
	}
	return ""
}

// snippetOf 는 조각에서 질의 낱말이 처음 나오는 자리 둘레의 발췌다(n 글자 안팎). 낱말이 없으면 앞부분.
func snippetOf(text, query string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return strings.TrimSpace(text)
	}
	low := []rune(strings.ToLower(text))
	pos := -1
	for _, w := range textindex.QueryWords(query) {
		for _, t := range w.Set {
			if i := indexRunes(low, []rune(t)); i >= 0 && (pos < 0 || i < pos) {
				pos = i
			}
		}
	}
	start := 0
	if pos > n/4 {
		start = pos - n/4
	}
	if start+n > len(r) {
		start = len(r) - n
	}
	if start < 0 {
		start = 0
	}
	out := strings.TrimSpace(string(r[start : start+n]))
	if start > 0 {
		out = "…" + out
	}
	if start+n < len(r) {
		out += "…"
	}
	return out
}

func indexRunes(hay, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// viewer 는 업로드 문서 열람 판정에 쓰는 호출자 정보다(실효 권한과 부서 이름). 부서 이름을 못 찾으면 비어 있고 부서 전용 문서는 안 보인다(fail-closed).
func (s *Service) viewer(ctx context.Context, p contract.Principal) (policysearch.Viewer, error) {
	v := policysearch.Viewer{Perms: policysearch.ViewerPerms(p)}
	if p.DepartmentID != 0 {
		name, err := s.repo.DepartmentName(ctx, s.DB, p.DepartmentID)
		if err != nil {
			return v, err
		}
		v.Dept = name
	}
	return v, nil
}

// uploadAllowed 는 업로드 문서를 DB 행에서 다시 읽어 열람 가능한지 독립으로 판정한다(삭제·교체됨·법인·역할 AND 부서). 없으면 거짓.
func (s *Service) uploadAllowed(ctx context.Context, id int64, v policysearch.Viewer, ent int64, scoped bool) (bool, error) {
	u, err := s.repo.UploadByID(ctx, s.DB, id, false)
	if errors.Is(err, contract.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if u.RemovedAt != "" || (scoped && u.EntityID != 0 && u.EntityID != ent) {
		return false, nil
	}
	return policysearch.Doc{RawRoles: u.Roles, RawDepts: u.Departments}.AllowsRaw(v), nil
}

// openUpload 는 원문 보기·내려받기의 공통 열람 판정이다. 권한 밖·삭제됨·모르는 번호는 모두 같은 ErrNotFound 다.
func (s *Service) openUpload(ctx context.Context, id int64, content bool, action string) (repository.UploadRow, contract.Principal, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return repository.UploadRow{}, p, err
	}
	ent, scoped := entityScope(ctx)
	v, err := s.viewer(ctx, p)
	if err != nil {
		return repository.UploadRow{}, p, err
	}
	u, err := s.repo.UploadByID(ctx, s.DB, id, content)
	if err != nil {
		return repository.UploadRow{}, p, err
	}
	if ok, err := s.uploadAllowed(ctx, id, v, ent, scoped); err != nil {
		return repository.UploadRow{}, p, err
	} else if !ok {
		return repository.UploadRow{}, p, contract.ErrNotFound
	}
	if err := s.record(ctx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{{Key: "kind", New: u.Kind}, {Key: "version", New: strconv.Itoa(u.Version)}}}); err != nil {
		return repository.UploadRow{}, p, err
	}
	return u, p, nil
}

// Open 은 domain.KnowledgeService 를 구현한다(업로드 문서 원문 보기). 원문은 열람 범위 안의 사람에게 그대로 보이고 열람은 감사에 남는다(발췌만 마스킹한다).
func (s *Service) Open(ctx context.Context, id int64) (domain.KnowledgeDocView, error) {
	u, _, err := s.openUpload(ctx, id, false, "knowledge.open")
	if err != nil {
		return domain.KnowledgeDocView{}, err
	}
	chunks, err := s.repo.UploadDocChunks(ctx, s.DB, id)
	if err != nil {
		return domain.KnowledgeDocView{}, err
	}
	var texts []string
	for _, c := range chunks {
		texts = append(texts, c.Heading, c.Text)
	}
	if err := s.notePII(ctx, privacy.ScreenKnowledgeDoc, texts); err != nil {
		return domain.KnowledgeDocView{}, err
	}
	view := domain.KnowledgeDocView{ID: u.ID, Kind: u.Kind, Title: u.Title, Version: u.Version, UploadedAt: u.UploadedAt, Filename: u.Filename}
	for _, c := range chunks {
		view.Sections = append(view.Sections, domain.KnowledgeSection{LocatorKind: c.LocatorKind, Locator: c.Locator, Heading: c.Heading, Text: c.Text})
	}
	return view, nil
}

// File 은 domain.KnowledgeService 를 구현한다(업로드 원본 내려받기).
func (s *Service) File(ctx context.Context, id int64) (domain.KnowledgeFile, error) {
	u, _, err := s.openUpload(ctx, id, true, "knowledge.download")
	if err != nil {
		return domain.KnowledgeFile{}, err
	}
	chunks, err := s.repo.UploadDocChunks(ctx, s.DB, id)
	if err != nil {
		return domain.KnowledgeFile{}, err
	}
	var texts []string
	for _, c := range chunks {
		texts = append(texts, c.Heading, c.Text)
	}
	if err := s.notePII(ctx, privacy.ScreenKnowledgeFile, texts); err != nil {
		return domain.KnowledgeFile{}, err
	}
	return domain.KnowledgeFile{Filename: u.Filename, ContentType: u.ContentType, Data: u.Content}, nil
}

// ErrPrivacyNote 는 열람 기록을 남기지 못해 원문을 보여 줄 수 없다는 뜻이다(핸들러는 500 으로 옮긴다).
var ErrPrivacyNote = errors.New("열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요")

// notePII 는 원문에 이메일·전화·계좌 패턴이 있으면 개인정보 열람 기록(B41 ①)을 응답 전에 남긴다. 정보주체는 알 수 없어 패턴 개수를 식별하지 못한 정보주체 수로 센다.
// 범주는 연락처(이메일·전화)와 계좌로 나눠 기록한다. 기록하지 못하면 ErrPrivacyNote(원문을 싣지 않는다).
func (s *Service) notePII(ctx context.Context, screen string, texts []string) error {
	var c rbac.PIICount
	for _, t := range texts {
		d := rbac.DetectPII(t)
		c.Email, c.Phone, c.Account = c.Email+d.Email, c.Phone+d.Phone, c.Account+d.Account
	}
	if !c.Any() {
		return nil
	}
	p, _ := contract.PrincipalFrom(ctx)
	var accesses []privacy.Access
	if c.Email+c.Phone > 0 {
		a := privacy.Access{Category: privacy.CatContact, Screen: screen, UnknownSubjects: c.Email + c.Phone}
		if c.Email > 0 {
			a.Fields = append(a.Fields, privacy.FieldEmail)
		}
		if c.Phone > 0 {
			a.Fields = append(a.Fields, privacy.FieldPhone)
		}
		accesses = append(accesses, a)
	}
	if c.Account > 0 {
		accesses = append(accesses, privacy.Access{Category: privacy.CatAccount, Screen: screen, Fields: []string{privacy.FieldAccountNo}, UnknownSubjects: c.Account})
	}
	if s.Privacy == nil {
		return ErrPrivacyNote
	}
	for _, a := range accesses {
		if err := s.Privacy.Note(ctx, p, a); err != nil {
			log.Printf("knowledge: 열람 기록 실패 화면=%s 범주=%s: %v", screen, a.Category, err)
			return ErrPrivacyNote
		}
	}
	return nil
}
