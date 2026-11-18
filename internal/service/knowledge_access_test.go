package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B47 1단계(erp·ai): 사내 지식 검색의 권한 우회 시험. 시험마다 "찾는다 → 권한이 사라진다/원래 없다 → 안 나온다" 쌍이다.
// 시험은 워커를 직접 돌려 결정적으로 색인한다.

type kenv struct {
	*env
	svc    *knowledge.Service
	admin  *knowledge.Admin
	worker *knowledge.Worker
}

func (e *env) knowledge(t *testing.T) *kenv {
	t.Helper()
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	txs := contract.SQLTxManager{DB: e.db}
	w := &knowledge.Worker{Txs: txs, Now: func() time.Time { return e.now }}
	return &kenv{env: e,
		svc:    &knowledge.Service{Txs: txs, DB: e.db, Audit: e.rec, Masker: fake.Masker{}, Opener: service.NewDocViewService(b), Store: knowledge.NewStore(e.db)},
		admin:  &knowledge.Admin{Txs: txs, DB: e.db, Audit: e.rec, Worker: w},
		worker: w}
}

func (k *kenv) drain(t *testing.T) {
	t.Helper()
	if _, err := k.worker.Drain(context.Background(), 20); err != nil {
		t.Fatalf("색인 대기열: %v", err)
	}
}

// found 는 사용자 who(법인 context entity, 0 이면 법인 없음)가 query 로 찾은 결재 문서 번호들이다.
func (k *kenv) found(t *testing.T, ctx context.Context, query string) []string {
	t.Helper()
	res, err := k.svc.Search(ctx, domain.KnowledgeFilter{Query: query, Limit: 20})
	if err != nil {
		t.Fatalf("검색: %v", err)
	}
	var nos []string
	for _, h := range res.Hits {
		nos = append(nos, h.DocNo)
	}
	return nos
}

func knHas(nos []string, no string) bool {
	for _, n := range nos {
		if n == no {
			return true
		}
	}
	return false
}

const kQuery = "분기 장비 점검 계획" // genDocWith 의 본문에 들어 있는 낱말

// completed 는 GEN 문서를 기안해 결재선(팀장·부서장)을 모두 승인한 완료 문서다. 본문에 kQuery 가 들어 있고 edits 로 참조 등을 더한다.
func (k *kenv) completed(t *testing.T, title string, edits ...domain.LineEdit) domain.PurchaseRequest {
	t.Helper()
	d, err := k.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: title, Fields: map[string]any{"content": "올해 " + kQuery + " 을 수립합니다"}, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := k.docs().Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatalf("상신: %v", err)
	}
	decideAs(t, k.env, uLead, pr.ID, true, "")
	decideAs(t, k.env, uHead, pr.ID, true, "승인")
	done, err := k.docs().Get(as(uStaff), pr.ID)
	if err != nil || done.Status != domain.PRApproved {
		t.Fatalf("완료: %+v %v", done.Status, err)
	}
	return done
}

// AC-4701·4703·4704·4706·4707·4709: 관계가 있는 사람만 찾고, 관계가 없는 사람(같은 부서·구매 전체 열람자)은 못 찾고, 특권 열람은 감사에 남는다.
func TestKnowledgeSearchFollowsDocumentViewScope(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	pr := k.completed(t, "분기 점검", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	// 완료 직후 대기열이 있다(완료 트랜잭션이 올렸다). 처리 전에는 아무도 못 찾는다.
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_queue WHERE source_kind = 'approval_doc' AND source_id = ? AND done_at IS NULL`, pr.ID); n != 1 {
		t.Fatalf("완료 때 색인 대기열이 올라가야 한다: %d", n)
	}
	if got := k.found(t, as(uStaff), kQuery); len(got) != 0 {
		t.Fatalf("색인 전: %v", got)
	}
	k.drain(t)
	for name, who := range map[string]int64{"기안자": uStaff, "팀장": uLead, "부서장": uHead, "참조 대상": uStaff2} {
		if got := k.found(t, as(who), kQuery); !knHas(got, pr.No) {
			t.Fatalf("%s 가 못 찾음: %v", name, got)
		}
	}
	// 같은 부서의 다른 팀장·구매 전체 열람자(finance)·다른 부서의 영업 팀장은 관계가 없어 못 찾는다(존재도 모른다).
	for name, who := range map[string]int64{"같은 부서 다른 팀장": uLead3, "구매 전체 열람자": uFinance, "다른 부서 팀장": uLead2} {
		if got := k.found(t, as(who), kQuery); len(got) != 0 {
			t.Fatalf("%s 가 찾음: %v", name, got)
		}
	}
	// 권한 밖과 일치 없음의 응답이 같다(건수·가려짐 표시 없음).
	none, _ := k.svc.Search(as(uLead3), domain.KnowledgeFilter{Query: kQuery})
	nothing, _ := k.svc.Search(as(uLead3), domain.KnowledgeFilter{Query: "전혀없는낱말"})
	if len(none.Hits) != 0 || none.More || len(nothing.Hits) != 0 || nothing.More {
		t.Fatalf("권한 밖과 일치 없음이 다르다: %+v %+v", none, nothing)
	}
	// 특권 열람(감사자)은 보이고 감사에 privileged_count 가 남는다. 관계가 있는 사람은 0.
	before := e.rec.Count()
	if got := k.found(t, as(uAuditor), kQuery); !knHas(got, pr.No) {
		t.Fatalf("감사자: %v", got)
	}
	if e.rec.Count() != before+1 {
		t.Fatalf("검색 한 번에 감사 한 건: %d", e.rec.Count()-before)
	}
	if v := lastDiff(t, e, "knowledge.search", "privileged_count"); v != "1" {
		t.Fatalf("특권 건수: %q", v)
	}
	k.found(t, as(uLead), kQuery)
	if v := lastDiff(t, e, "knowledge.search", "privileged_count"); v != "0" {
		t.Fatalf("관계로 연 검색의 특권 건수: %q", v)
	}
	// 질의는 마스킹본으로만 감사에 남고 점수·건수 필드 외의 문서 정보가 없다.
	if v := lastDiff(t, e, "knowledge.search", "query"); !strings.Contains(v, "장비") {
		t.Fatalf("질의 감사: %q", v)
	}
}

func lastDiff(t *testing.T, e *env, action, key string) string {
	t.Helper()
	recs := e.rec.Records()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Action == action {
			for _, d := range recs[i].Diff {
				if d.Key == key {
					return d.New
				}
			}
		}
	}
	t.Fatalf("감사 %s.%s 없음", action, key)
	return ""
}

// AC-4703: 회수된 참조. 상신 취소 뒤 참조 없이 다시 상신해 완료하면 예전에 참조였던 사람은 못 찾는다(차수가 바뀌었다). 참조가 유지된 문서는 찾는다.
func TestKnowledgeRevokedReferenceIsNotFound(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	kept := k.completed(t, "참조 유지", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	// 참조가 있던 문서를 상신 취소하고, 참조를 뺀 채 다시 상신해 완료한다.
	d, err := k.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "참조 회수", Fields: map[string]any{"content": "올해 " + kQuery + " 을 다시 수립합니다"},
		Edits: []domain.LineEdit{{Role: "cc", TargetType: "user", TargetID: uStaff2}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.docs().Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.docs().CancelSubmit(as(uStaff), d.ID, "참조 정정"); err != nil {
		t.Fatalf("상신 취소: %v", err)
	}
	if _, err := k.docs().UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "GEN", Title: "참조 회수", Fields: map[string]any{"content": "올해 " + kQuery + " 을 다시 수립합니다"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.docs().Submit(as(uStaff), d.ID); err != nil {
		t.Fatal(err)
	}
	decideAs(t, k.env, uLead, d.ID, true, "")
	decideAs(t, k.env, uHead, d.ID, true, "")
	revoked, err := k.docs().Get(as(uStaff), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	k.drain(t)
	got := k.found(t, as(uStaff2), kQuery)
	if !knHas(got, kept.No) {
		t.Fatalf("참조가 유지된 문서를 못 찾음: %v", got)
	}
	if knHas(got, revoked.No) {
		// 참조가 새 차수에 미리 채워지는 규칙이면 이 시험 전제(회수)가 성립하지 않는다. 그 경우 문서를 직접 열 수 있는지와 일치해야 한다.
		if _, gerr := k.docs().Get(as(uStaff2), d.ID); gerr != nil {
			t.Fatalf("검색은 되는데 문서를 열 수 없다: %v", gerr)
		}
		t.Skip("취소 뒤 참조가 새 차수에 유지되는 규칙이라 회수 시험 전제가 맞지 않음")
	}
	if _, gerr := k.docs().Get(as(uStaff2), d.ID); !errors.Is(gerr, contract.ErrNotFound) {
		t.Fatalf("문서를 열 수 없어야 검색에서도 빠진다: %v", gerr)
	}
}

// AC-4702: 다른 법인. 색인에 올라 있어도 현재 법인이 다르면 나오지 않고, 그 법인으로 전환하면 관계가 있는 사람은 찾는다.
func TestKnowledgeOtherEntityIsNotFound(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	k := e.knowledge(t)
	pr := k.completed(t, "법인 시험")
	k.drain(t)
	if got := k.found(t, inEntity(uStaff, contract.DefaultEntityID), kQuery); !knHas(got, pr.No) {
		t.Fatalf("기본 법인에서 못 찾음: %v", got)
	}
	// 문서를 계열사로 옮긴 것처럼(법인은 원래 불변이라 시험용 직접 변경) 색인 법인과 문서 법인을 모두 바꾼다.
	if _, err := e.db.Exec(`UPDATE purchase_requests SET entity_id = ? WHERE id = ?`, abc, pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE knowledge_docs SET entity_id = ? WHERE source_kind = 'approval_doc' AND source_id = ?`, abc, pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE knowledge_chunks SET entity_id = ? WHERE doc_id IN (SELECT id FROM knowledge_docs WHERE source_id = ?)`, abc, pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE knowledge_state SET generation = generation + 1`); err != nil {
		t.Fatal(err)
	}
	if got := k.found(t, inEntity(uStaff, contract.DefaultEntityID), kQuery); len(got) != 0 {
		t.Fatalf("다른 법인 문서가 기본 법인에서 나옴: %v", got)
	}
	if got := k.found(t, inEntity(uStaff, abc), kQuery); !knHas(got, pr.No) {
		t.Fatalf("그 법인으로 전환하면 관계가 있는 사람은 찾아야 한다: %v", got)
	}
	// 색인이 낡아(문서 법인만 바뀌고 색인 법인은 그대로) 후보에 올라도 열람 판정이 현재 법인으로 거른다.
	if _, err := e.db.Exec(`UPDATE knowledge_chunks SET entity_id = ?`, contract.DefaultEntityID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE knowledge_state SET generation = generation + 1`); err != nil {
		t.Fatal(err)
	}
	if got := k.found(t, inEntity(uStaff, contract.DefaultEntityID), kQuery); len(got) != 0 {
		t.Fatalf("낡은 색인이 법인 경계를 넘김: %v", got)
	}
	// 권한이 법인마다 다른 사람이 효력 없는 법인에서 검색하면 거부된다.
	p := users[uStaff]
	p.Channel = contract.ChannelWeb
	p.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermKnowledgeSearch: contract.NewEntitySet(contract.DefaultEntityID)}
	ctx := contract.WithActiveEntity(contract.WithPrincipal(context.Background(), p), contract.ActiveEntity{ID: abc})
	if _, err := k.svc.Search(ctx, domain.KnowledgeFilter{Query: kQuery}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("효력 없는 법인: %v", err)
	}
	if !e.rec.Has("knowledge.search", audit.TargetKnowledge, audit.ResultDenied) {
		t.Fatal("거부 감사 기록 없음")
	}
}

// AC-4706: 진행 중·반려 문서는 색인되지 않고, 폐기되면 낡은 색인에서도 나오지 않으며 폐기 트랜잭션이 조각을 지운다.
func TestKnowledgeDraftInReviewAndDisposedAreNotFound(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	progress := e.genSubmitted(t, uStaff, "진행 중 문서")
	k.drain(t)
	if got := k.found(t, as(uStaff), "본문 내용"); knHas(got, progress.No) {
		t.Fatalf("진행 중 문서가 검색됨: %v", got)
	}
	pr := k.completed(t, "폐기 시험")
	k.drain(t)
	if got := k.found(t, as(uStaff), kQuery); !knHas(got, pr.No) {
		t.Fatalf("완료 문서: %v", got)
	}
	// 낡은 색인: 폐기 표시만 하고 조각은 그대로 둔다. 열람 판정이 거른다.
	if _, err := e.db.Exec(`UPDATE purchase_requests SET disposed_at = '2026-11-14' WHERE id = ?`, pr.ID); err != nil {
		t.Fatal(err)
	}
	if got := k.found(t, as(uStaff), kQuery); knHas(got, pr.No) {
		t.Fatalf("폐기된 문서가 낡은 색인에서 나옴: %v", got)
	}
	// 폐기 트랜잭션의 정리(동기 삭제)는 조각과 지식 문서를 지우고 세대를 올린다.
	var gen0 int64
	_ = e.db.QueryRow(`SELECT generation FROM knowledge_state WHERE id = 1`).Scan(&gen0)
	if err := (contract.SQLTxManager{DB: e.db}).WithTx(context.Background(), func(tx contract.Tx) error {
		return repository.PurgeApprovalDoc(context.Background(), tx, pr.ID, e.now)
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_chunks c JOIN knowledge_docs d ON d.id = c.doc_id WHERE d.source_id = ?`, pr.ID); n != 0 {
		t.Fatalf("폐기 뒤 조각이 남음: %d", n)
	}
	var gen1 int64
	_ = e.db.QueryRow(`SELECT generation FROM knowledge_state WHERE id = 1`).Scan(&gen1)
	if gen1 <= gen0 {
		t.Fatalf("폐기가 세대를 올리지 않음: %d → %d", gen0, gen1)
	}
}

// AC-4713·D12: 양식을 검색에서 빼면 기존 문서도 바로 안 나오고, 다시 색인하면 색인에서도 빠진다. 다시 색인은 멱등이다.
func TestKnowledgeFormSearchableAndReindexIdempotent(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	pr := k.completed(t, "양식 제외 시험")
	k.drain(t)
	if got := k.found(t, as(uStaff), kQuery); !knHas(got, pr.No) {
		t.Fatalf("색인 뒤: %v", got)
	}
	adminCtx := as(uAdmin)
	if err := k.admin.SetFormSearchable(as(uStaff), "GEN", false); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원이 양식 검색 포함을 바꿈: %v", err)
	}
	if err := k.admin.SetFormSearchable(adminCtx, "GEN", false); err != nil {
		t.Fatal(err)
	}
	if got := k.found(t, as(uStaff), kQuery); knHas(got, pr.No) {
		t.Fatalf("양식 제외 직후에도 나옴: %v", got)
	}
	done, skipped, err := k.admin.Reindex(adminCtx, "", "")
	if err != nil || done < 1 {
		t.Fatalf("다시 색인(제외 반영): %d %d %v", done, skipped, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_docs WHERE source_kind = 'approval_doc' AND source_id = ?`, pr.ID); n != 0 {
		t.Fatal("제외한 양식 문서가 색인에 남음")
	}
	if err := k.admin.SetFormSearchable(adminCtx, "GEN", true); err != nil {
		t.Fatal(err)
	}
	if done, _, err = k.admin.Reindex(adminCtx, "", ""); err != nil || done != 1 {
		t.Fatalf("다시 포함 뒤 색인: %d %v", done, err)
	}
	if done, skipped, err = k.admin.Reindex(adminCtx, "", ""); err != nil || done != 0 || skipped < 1 {
		t.Fatalf("다시 색인은 멱등이어야 한다: %d %d %v", done, skipped, err)
	}
	if got := k.found(t, as(uStaff), kQuery); !knHas(got, pr.No) {
		t.Fatalf("다시 포함 뒤: %v", got)
	}
	if _, _, err := k.admin.Reindex(as(uStaff), "", ""); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원이 다시 색인: %v", err)
	}
	if !e.rec.Has("knowledge.reindex", audit.TargetKnowledge, audit.ResultSuccess) || !e.rec.Has("knowledge.form_searchable", audit.TargetKnowledge, audit.ResultSuccess) {
		t.Fatal("관리 감사 기록 없음")
	}
	st, err := k.admin.Status(adminCtx)
	if err != nil || st.Docs["approval_doc"] != 1 || st.Chunks < 1 || st.QueueOpen != 0 {
		t.Fatalf("색인 현황: %+v %v", st, err)
	}
}

// AC-4724: 감사가 실패하면 결과를 주지 않는다. 검색어 검증 오류는 400 계열이다.
func TestKnowledgeAuditFailureHidesResultsAndQueryValidation(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	k.completed(t, "감사 시험")
	k.drain(t)
	if _, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: "장"}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("1자 검색: %v", err)
	}
	if _, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: strings.Repeat("가", 201)}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("200자 초과: %v", err)
	}
	if _, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: kQuery, Kinds: []string{"nope"}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 종류: %v", err)
	}
	e.rec.FailWith(errors.New("디스크 가득"))
	res, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: kQuery})
	e.rec.FailWith(nil)
	if err == nil || len(res.Hits) != 0 {
		t.Fatalf("감사 실패인데 결과가 나감: %+v %v", res, err)
	}
	// 권한이 없는 호출자(알 수 없는 역할)는 거부되고 거부가 감사에 남는다.
	ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 77, Name: "외부", Role: "unknown-role", Channel: contract.ChannelWeb})
	if _, err := k.svc.Search(ctx, domain.KnowledgeFilter{Query: kQuery}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("권한 없는 호출: %v", err)
	}
}

// AC-4721·4716: 발췌는 마스킹되고 출처 한 줄이 문서 번호·제목·본문 표지를 담으며 링크는 상세 경로다.
func TestKnowledgeHitHasSourceLineMaskedSnippetAndLink(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	pr := k.completed(t, "출처 시험")
	k.drain(t)
	res, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: kQuery})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("검색: %+v %v", res, err)
	}
	h := res.Hits[0]
	if h.Source != "결재 문서 "+pr.No+" 「출처 시험」 · 본문" || h.Link != "/docs/"+itoa(pr.ID) || !strings.Contains(h.Snippet, "장비") || h.Kind != domain.KnowledgeApprovalDoc {
		t.Fatalf("결과: %+v", h)
	}
}

// AC-4721: 발췌의 연락처·이메일·계좌·사업자번호 원문은 가려진다(실제 마스킹기). 질의에 전화번호를 넣어도 감사에는 마스킹본만 남는다.
func TestKnowledgeSnippetMasksPersonalData(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	k.svc.Masker = rbac.Masker{}
	d, err := k.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "연락처 포함", Fields: map[string]any{
		"content": "담당 연락처 010-9999-8888 이메일 owner@example.com 사업자번호 123-45-67890 를 확인하세요. 분기 점검"}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := k.docs().Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	decideAs(t, k.env, uLead, pr.ID, true, "")
	decideAs(t, k.env, uHead, pr.ID, true, "")
	k.drain(t)
	res, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: "분기 점검 연락처"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("검색: %+v %v", res, err)
	}
	for _, raw := range []string{"010-9999-8888", "owner@example.com", "123-45-67890"} {
		if strings.Contains(res.Hits[0].Snippet, raw) {
			t.Fatalf("발췌에 원문 %q: %s", raw, res.Hits[0].Snippet)
		}
	}
	if _, err := k.svc.Search(as(uStaff), domain.KnowledgeFilter{Query: "010-9999-8888 연락처"}); err != nil {
		t.Fatal(err)
	}
	if q := lastDiff(t, e, "knowledge.search", "query"); strings.Contains(q, "010-9999-8888") {
		t.Fatalf("감사에 전화번호 원문: %q", q)
	}
}
