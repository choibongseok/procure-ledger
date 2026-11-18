package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) docs() *service.ApprovalDocService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewApprovalDocService(b)
}

func (e *env) genDraft(t *testing.T, by int64, title string, edits ...domain.LineEdit) domain.PurchaseRequest {
	t.Helper()
	pr, err := e.docs().CreateDraft(as(by), domain.DocDraftInput{FormCode: "GEN", Title: title, Fields: map[string]any{"content": "본문 내용"}, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func (e *env) genSubmitted(t *testing.T, by int64, title string, edits ...domain.LineEdit) domain.PurchaseRequest {
	t.Helper()
	d := e.genDraft(t, by, title, edits...)
	pr, err := e.docs().Submit(as(by), d.ID)
	if err != nil {
		t.Fatalf("상신: %v", err)
	}
	return pr
}

func decideAs(t *testing.T, e *env, who, id int64, approve bool, comment string) domain.DecisionResult {
	t.Helper()
	d := domain.Decision{Approve: approve, Comment: comment}
	var res domain.DecisionResult
	var err error
	if users[who].Can(contract.PermApprovalDecide) {
		res, err = e.approval.Decide(as(who), id, d)
	} else {
		res, err = e.approval.Agree(as(who), id, d)
	}
	if err != nil {
		t.Fatalf("처리(%d): %v", who, err)
	}
	return res
}

func stepStatuses(pr domain.PurchaseRequest) []string {
	var out []string
	for _, s := range pr.Steps {
		out = append(out, s.Role+":"+string(s.Status))
	}
	return out
}

// 일반 기안서: 필드 검증, 번호 채번(부서·연도·이어짐), 팀장 → 부서장 결재, 완료 시 보존 기한과 완료 시각, 발주 없음.
func TestGeneralDocFlowAndNumbering(t *testing.T) {
	e := newEnv(t)
	// 필수 본문이 없으면 상신이 필드 오류로 거부된다(임시저장은 된다).
	d, err := e.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "빈 기안", Fields: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.No, "TMP-") {
		t.Fatalf("임시 번호 아님: %s", d.No)
	}
	_, err = e.docs().Submit(as(uStaff), d.ID)
	fe, ok := service.IsFieldErrors(err)
	if !ok || !strings.Contains(fe.ByKey()["content"], "「본문」을(를) 입력하세요.") {
		t.Fatalf("필수 검사: %v", err)
	}
	if _, err := e.docs().UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "GEN", Title: "빈 기안", Fields: map[string]any{"content": "내용"}}); err != nil {
		t.Fatal(err)
	}
	pr, err := e.docs().Submit(as(uStaff), d.ID)
	if err != nil || pr.No != "구매팀-2026-0001" || pr.Status != domain.PRSubmitted || pr.FormCode != "GEN" {
		t.Fatalf("상신: %+v %v", pr, err)
	}
	if got := stepStatuses(pr); len(got) != 2 || got[0] != "approve:pending" || got[1] != "approve:waiting" {
		t.Fatalf("결재선 상태: %v", got)
	}
	// 같은 부서·연도의 다음 문서는 번호가 이어진다.
	pr2 := e.genSubmitted(t, uStaff, "둘째")
	if pr2.No != "구매팀-2026-0002" {
		t.Fatalf("이어진 번호: %s", pr2.No)
	}
	// 결재: 팀장 → 부서장. 마지막 승인이 결재 완료이고 발주는 없다.
	decideAs(t, e, uLead, pr.ID, true, "")
	res := decideAs(t, e, uHead, pr.ID, true, "승인합니다")
	if res.PurchaseOrder != nil || res.Request.Status != domain.PRApproved {
		t.Fatalf("완료: %+v", res.Request.Status)
	}
	done, _ := e.docs().Get(as(uStaff), pr.ID)
	if done.RetentionUntil != "2031-11-10" {
		t.Fatalf("보존 기한 %q", done.RetentionUntil)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM purchase_orders WHERE pr_id = ?`, pr.ID); n != 0 {
		t.Fatal("양식 문서에 발주가 생김")
	}
	if !e.rec.Has("approval_doc.submit", "approval_doc", "success") || !e.rec.Has("approval.approve", "approval", "success") {
		t.Fatal("감사 기록 없음")
	}
}

// AC-121 연도 경계: 12/31 23:59:59 KST 상신은 그 해, 다음 1초는 새 해 1번이다.
func TestDocNumberYearBoundary(t *testing.T) {
	e := newEnv(t)
	kst := time.FixedZone("KST", 9*3600)
	e.now = time.Date(2026, 12, 31, 23, 59, 59, 0, kst)
	a := e.genSubmitted(t, uStaff, "연말")
	e.now = time.Date(2027, 1, 1, 0, 0, 0, 0, kst)
	b := e.genSubmitted(t, uStaff, "새해")
	if a.No != "구매팀-2026-0001" || b.No != "구매팀-2027-0001" {
		t.Fatalf("연말·새해 번호: %s %s", a.No, b.No)
	}
}

// AC-122 부서 이름이 바뀐 뒤 새 문서는 새 이름·이어진 번호를 받고 이미 채번한 번호는 그대로다.
func TestDocNumberSurvivesUnitRename(t *testing.T) {
	e := newEnv(t)
	a := e.genSubmitted(t, uStaff, "개명 전")
	e.org.AddUnit(unitBuy, "BUY", "구매본팀", contract.OrgDepartment, unitCompany)
	b := e.genSubmitted(t, uStaff, "개명 후")
	if a.No != "구매팀-2026-0001" || b.No != "구매본팀-2026-0002" {
		t.Fatalf("번호: %s %s", a.No, b.No)
	}
	got, _ := e.docs().Get(as(uStaff), a.ID)
	if got.No != a.No {
		t.Fatal("이미 채번한 번호가 바뀜")
	}
}

// AC-131 결재선 편집 규칙: 기안자·중복·권한 없는 합의자·편집 범위 밖.
func TestLineEditRules(t *testing.T) {
	e := newEnv(t)
	preview := func(form string, edits ...domain.LineEdit) domain.LinePreview {
		pv, err := e.docs().PreviewLine(as(uStaff), domain.DocLineInput{FormCode: form, Edits: edits})
		if err != nil {
			t.Fatal(err)
		}
		return pv
	}
	has := func(pv domain.LinePreview, sub string) bool {
		for _, p := range pv.Problems {
			if strings.Contains(p, sub) {
				return true
			}
		}
		return false
	}
	if pv := preview("GEN", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uStaff}); pv.Complete || !has(pv, "기안자는 결재선에 넣을 수 없습니다.") {
		t.Fatalf("기안자 합의: %+v", pv.Problems)
	}
	if pv := preview("GEN", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead}); pv.Complete || !has(pv, "팀장님은 한 문서에서 한 단계만 맡을 수 있습니다.") {
		t.Fatalf("중복: %+v", pv.Problems)
	}
	if pv := preview("GEN", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uAuditor}); pv.Complete || !has(pv, "합의 권한이 없어 합의자로 넣을 수 없습니다.") {
		t.Fatalf("권한 없는 합의: %+v", pv.Problems)
	}
	// 구매 품의서가 아닌 add_only 양식(EXP)에서는 결재 단계를 더할 수 없다.
	if pv := preview("EXP", domain.LineEdit{Role: "approve", TargetType: "user", TargetID: uFinance}); pv.Complete || !has(pv, "결재 단계를 더할 수 없습니다") {
		t.Fatalf("add_only: %+v", pv.Problems)
	}
	// 정상: 합의 추가, 참조 추가(기안자 참조 제외 안내, 결재선에 있는 사람 참조는 자동 제외).
	pv := preview("GEN", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance},
		domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2}, domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uHead})
	if !pv.Complete || len(pv.Steps) != 3 || pv.Steps[2].Role != "agree" || pv.Steps[2].ApproverID != uFinance {
		t.Fatalf("정상 편집: %+v %+v", pv.Steps, pv.Problems)
	}
	if len(pv.Recipients) != 1 || pv.Recipients[0].TargetID != uStaff2 {
		t.Fatalf("참조: %+v", pv.Recipients)
	}
	found := false
	for _, n := range pv.Notes {
		found = found || strings.Contains(n, "부서장님은 결재선에 이미 있어 참조에서 뺐습니다.")
	}
	if !found {
		t.Fatalf("자동 제외 비고 없음: %v", pv.Notes)
	}
	// 병렬 합의는 5명까지.
	var many []domain.LineEdit
	for _, id := range []int64{uFinance, uFinance2, uLead3, uCEO, uAdmin, uAuditor} {
		many = append(many, domain.LineEdit{Role: "agree", TargetType: "user", TargetID: id, Group: 1})
	}
	if pv := preview("GEN", many...); pv.Complete {
		t.Fatal("병렬 합의 6명이 통과함")
	}
}

// AC-133 병렬 합의: 같은 단계의 합의가 동시에 pending 이고 모두 처리돼야 다음이 열린다. 이견은 record 양식이면 진행한다.
func TestParallelAgreeAndObjectionRecord(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "병렬", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance, Group: 1},
		domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3, Group: 1})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	cur, _ := e.docs().Get(as(uStaff), pr.ID)
	if got := stepStatuses(cur); got[2] != "agree:pending" || got[3] != "agree:pending" {
		t.Fatalf("병렬 합의가 동시에 열리지 않음: %v", got)
	}
	// 이견은 사유가 필요하다.
	if _, err := e.approval.Decide(as(uFinance), pr.ID, domain.Decision{Approve: false}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("사유 없는 이견: %v", err)
	}
	res := decideAs(t, e, uFinance, pr.ID, false, "금액이 큽니다")
	if res.Request.Status != domain.PRInReview {
		t.Fatalf("이견 후 상태 %s(record 양식은 진행)", res.Request.Status)
	}
	res = decideAs(t, e, uLead3, pr.ID, true, "")
	if res.Request.Status != domain.PRApproved {
		t.Fatalf("모든 합의 후 상태 %s", res.Request.Status)
	}
	if got := stepStatuses(res.Request); got[2] != "agree:objected" || got[3] != "agree:agreed" {
		t.Fatalf("합의 결과: %v", got)
	}
	// 합의 이력은 동의·이견으로 남는다.
	hist, _ := e.approval.History(as(uStaff), pr.ID)
	acts := map[domain.ApprovalAction]int{}
	for _, h := range hist {
		acts[h.Action]++
	}
	if acts[domain.ActionAgree] != 1 || acts[domain.ActionObject] != 1 {
		t.Fatalf("이력: %+v", acts)
	}
}

// AC-134 이견을 반려로 처리하는 양식(협조전)은 이견 즉시 문서가 반려되고 처리하지 않은 합의는 건너뜀이 된다. 늦은 쪽의 동의는 거부된다(E25).
func TestObjectionRejectsAndSkipsSiblings(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "GEN")
	def := v.Definition
	def.Settings.ObjectionAction = "reject"
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	pr := e.genSubmitted(t, uStaff, "이견 반려", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uFinance, Group: 1},
		domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3, Group: 1})
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "")
	res := decideAs(t, e, uFinance, pr.ID, false, "반대")
	if res.Request.Status != domain.PRRejected || res.Request.RejectReason != "반대" {
		t.Fatalf("이견 반려: %s %q", res.Request.Status, res.Request.RejectReason)
	}
	if got := stepStatuses(res.Request); got[3] != "agree:skipped" {
		t.Fatalf("다른 합의자 단계: %v", got)
	}
	// 같은 순간 동의한 늦은 쪽은 거부된다.
	if _, err := e.approval.Decide(as(uLead3), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("반려된 문서에 합의 동의가 통과함")
	}
	if cur, _ := e.docs().Get(as(uStaff), pr.ID); cur.Status != domain.PRRejected {
		t.Fatalf("상태가 바뀜: %s", cur.Status)
	}
}

// AC-135 합의는 위임할 수 없다: 위임 수임자가 합의를 대신 처리하지 못하고 결재함에도 합의 건이 위임분으로 올라오지 않는다.
func TestAgreeCannotBeDelegated(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead3, uHead) // 구매2팀 팀장 → 부서장
	pr := e.genSubmitted(t, uStaff, "합의 위임", domain.LineEdit{Role: "agree", TargetType: "user", TargetID: uLead3})
	decideAs(t, e, uLead, pr.ID, true, "")
	// 부서장은 2단계(결재) 차례다. 합의 3단계는 아직 대기이므로 먼저 부서장이 결재한다.
	decideAs(t, e, uHead, pr.ID, true, "")
	// 이제 합의 차례. uHead 는 uLead3 의 수임자지만 한 사람 한 단계 규칙·합의 위임 불가로 처리할 수 없다.
	if _, err := e.approval.Decide(as(uHead), pr.ID, domain.Decision{Approve: true}); err == nil {
		t.Fatal("수임자가 합의를 대신 처리함")
	}
	list, _ := e.approval.ListPending(as(uHead))
	for _, p := range list {
		if p.ID == pr.ID {
			t.Fatal("합의 건이 위임분으로 결재함에 올라옴")
		}
	}
	decideAs(t, e, uLead3, pr.ID, true, "")
}

// AC-155 열람: 새 양식 문서는 부서 동료가 못 보고, 결재선 전원·참조 대상·특권 열람자(감사 기록)가 본다. 구매 API·MCP 에는 나오지 않는다.
func TestDocVisibilityAndPurchaseIsolation(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "열람", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	get := func(who int64) error { _, err := e.docs().Get(as(who), pr.ID); return err }
	for _, who := range []int64{uStaff, uLead, uHead, uStaff2} {
		if err := get(who); err != nil {
			t.Fatalf("사용자 %d 가 못 봄: %v", who, err)
		}
	}
	// 같은 구매팀(부서)의 다른 팀장·구매 전체 열람자(finance)는 관계가 없어 못 본다.
	for _, who := range []int64{uLead3, uFinance} {
		if err := get(who); !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("사용자 %d: %v", who, err)
		}
	}
	// 특권 열람(감사자·관리자)은 보이되 감사 기록이 남는다.
	before := e.rec.Count()
	if err := get(uAuditor); err != nil {
		t.Fatalf("감사자: %v", err)
	}
	if !e.rec.Has("approval_doc.view_privileged", "approval_doc", "success") || e.rec.Count() != before+1 {
		t.Fatal("특권 열람 감사 기록 없음")
	}
	before = e.rec.Count()
	_ = get(uStaff) // 당사자의 열람은 기록하지 않는다
	if e.rec.Count() != before {
		t.Fatal("당사자 열람이 감사에 남음")
	}
	// 참조 대상은 목록에도 보이고, 임시저장 문서의 참조 대상에게는 아직 보이지 않는다.
	list, _ := e.docs().List(as(uStaff2), domain.DocFilter{})
	if len(list) != 1 || list[0].ID != pr.ID {
		t.Fatalf("참조 대상 목록: %+v", list)
	}
	draft := e.genDraft(t, uStaff, "임시", domain.LineEdit{Role: "cc", TargetType: "user", TargetID: uStaff2})
	if _, err := e.docs().Get(as(uStaff2), draft.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("임시저장 참조: %v", err)
	}
	// 구매 API 와 MCP 채널에는 양식 문서가 나오지 않는다(AC-103).
	if _, err := e.purchase.Get(as(uStaff), pr.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("구매 API 가 양식 문서를 돌려줌: %v", err)
	}
	pl, _ := e.purchase.List(as(uStaff), domain.PRFilter{})
	for _, p := range pl {
		if p.ID == pr.ID {
			t.Fatal("구매 목록에 양식 문서가 있음")
		}
	}
	mcp := users[uLead]
	mcp.Channel = contract.ChannelMCP
	if _, err := e.approval.Decide(contract.WithPrincipal(context.Background(), mcp), pr.ID, domain.Decision{Approve: true}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("MCP 가 양식 문서를 결재함: %v", err)
	}
}

// AC-155 민감 필드: 당사자에게는 원문, 비관계 열람자(특권)와 MCP 채널에는 앞 두 글자만 보인다.
func TestSensitiveFieldMasking(t *testing.T) {
	e := newEnv(t)
	att, _ := e.attach()
	ev, err := att.Upload(as(uStaff), "영수증.pdf", pdfBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"spend_date": "2026-11-10", "spend_kind": "출장", "payee": "김가나다", "items": []any{map[string]any{"name": "택시", "amount": 15000}}, "total": 15000, "evidence": []any{float64(ev.ID)}}
	d, err := e.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "출장비", Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := e.docs().Submit(as(uStaff), d.ID)
	if err != nil {
		t.Fatalf("상신: %v", err)
	}
	if pr.Fields["payee"] != "김가나다" {
		t.Fatalf("당사자 원문: %v", pr.Fields["payee"])
	}
	got, err := e.docs().Get(as(uAuditor), pr.ID)
	if err != nil || got.Fields["payee"] != "김가**" {
		t.Fatalf("특권 열람자 마스킹: %v %v", got.Fields["payee"], err)
	}
	mcp := users[uStaff]
	mcp.Channel = contract.ChannelMCP
	own, _ := e.docs().Get(contract.WithPrincipal(context.Background(), mcp), pr.ID)
	if own.Fields["payee"] != "김가**" {
		t.Fatalf("MCP 채널은 당사자에게도 마스킹: %v", own.Fields["payee"])
	}
	// 지출 결의서는 금액 구간 규칙(양식 전용)으로 결재선이 만들어진다: 1만 5천 원은 팀장 한 단계.
	if len(pr.Steps) != 1 {
		t.Fatalf("지출 결의서 결재선: %+v", stepStatuses(pr))
	}
}

// AC-112 문서는 기안 때의 양식 버전으로 검증된다: 새 버전이 필드를 늘려도 임시저장 문서는 옛 정의다.
func TestDraftKeepsFormVersion(t *testing.T) {
	e := newEnv(t)
	d := e.genDraft(t, uStaff, "버전 고정")
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "GEN")
	def := v.Definition
	def.Fields = append(def.Fields, domain.FieldDef{Key: "memo", Label: "메모", Type: domain.FieldText})
	_, _ = f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def})
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	// 옛 버전 문서는 새 필드(memo)를 받지 않는다.
	_, err := e.docs().UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "GEN", Title: "버전 고정", Fields: map[string]any{"content": "x", "memo": "m"}})
	if fe, ok := service.IsFieldErrors(err); !ok || fe.ByKey()["memo"] == "" {
		t.Fatalf("옛 버전 문서가 새 필드를 받음: %v", err)
	}
	// 새로 기안하는 문서는 새 필드를 쓴다.
	n, err := e.docs().CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "GEN", Title: "새 버전", Fields: map[string]any{"content": "x", "memo": "m"}})
	if err != nil || n.Fields["memo"] != "m" {
		t.Fatalf("새 버전 문서: %v %v", n.Fields, err)
	}
	// 옛 버전 임시저장 문서도 상신할 수 있다(퇴역한 버전).
	if _, err := e.docs().UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "GEN", Title: "버전 고정", Fields: map[string]any{"content": "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.docs().Submit(as(uStaff), d.ID); err != nil {
		t.Fatalf("퇴역한 버전의 임시저장 문서 상신: %v", err)
	}
}

// AC-123 동시 상신: 서로 다른 문서 둘이 같은 번호를 받지 않는다(일련번호 표가 같은 트랜잭션에서 올라간다).
func TestConcurrentSubmitGetsDistinctNumbers(t *testing.T) {
	e := newEnv(t)
	a := e.genDraft(t, uStaff, "a")
	b := e.genDraft(t, uLead, "b") // 팀장 요청: 올림으로 부서장부터
	done := make(chan string, 2)
	for _, x := range []struct{ by, id int64 }{{uStaff, a.ID}, {uLead, b.ID}} {
		go func() {
			pr, err := e.docs().Submit(as(x.by), x.id)
			if err != nil {
				t.Error(err)
				done <- ""
				return
			}
			done <- pr.No
		}()
	}
	n1, n2 := <-done, <-done
	if n1 == "" || n2 == "" || n1 == n2 {
		t.Fatalf("번호 중복·실패: %q %q", n1, n2)
	}
}

// 반려 뒤 재상신: 처리하지 않은 단계는 새 차수로 다시 만들어지고 번호는 유지된다.
func TestResubmitKeepsNumber(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "반려 시험")
	decideAs(t, e, uLead, pr.ID, false, "보완 필요")
	cur, _ := e.docs().Get(as(uStaff), pr.ID)
	if cur.Status != domain.PRRejected {
		t.Fatalf("상태 %s", cur.Status)
	}
	again, err := e.docs().Submit(as(uStaff), pr.ID)
	if err != nil || again.No != pr.No || again.Round != 2 {
		t.Fatalf("재상신: %+v %v", again, err)
	}
	if got := stepStatuses(again); got[0] != "approve:pending" {
		t.Fatalf("새 차수 단계: %v", got)
	}
}
