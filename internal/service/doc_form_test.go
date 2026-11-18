package service_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) forms() *service.FormService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewFormService(b)
}

func jsonVals(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func expenseFields(t *testing.T, e *env) []domain.FieldDef {
	t.Helper()
	d, err := e.forms().Get(as(uStaff), "EXP")
	if err != nil {
		t.Fatal(err)
	}
	return d.Definition.Fields
}

// AC-110 기본 양식 4종이 시드되고 구매 품의서가 시스템 양식이다.
func TestSeedFormsAreUsable(t *testing.T) {
	e := newEnv(t)
	list, err := e.forms().ListUsable(as(uStaff))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.FormSummary{}
	for _, f := range list {
		got[f.Code] = f
	}
	for _, code := range []string{"PUR", "GEN", "EXP", "COOP"} {
		if got[code].Version < 1 {
			t.Fatalf("양식 %s 가 목록에 없음: %+v", code, list)
		}
	}
	pur, err := e.forms().Get(as(uStaff), "PUR")
	if err != nil || !pur.Definition.System || pur.Definition.Numbering.Pattern != "PR-{id:6}" || pur.Definition.Numbering.AssignAt != "create" {
		t.Fatalf("구매 품의서: %+v %v", pur, err)
	}
	exp, _ := e.forms().Get(as(uStaff), "EXP")
	if exp.Definition.Settings.AmountField != "total" || exp.Definition.RetentionYears != 5 || exp.Definition.Numbering.Pattern != "EXP-{yyyy}-{seq:5}" {
		t.Fatalf("지출 결의서: %+v", exp.Definition)
	}
	coop, _ := e.forms().Get(as(uStaff), "COOP")
	if coop.Definition.Settings.ObjectionAction != "reject" || coop.Definition.RetentionYears != 3 {
		t.Fatalf("협조전: %+v", coop.Definition)
	}
	// 감사자는 기안 권한이 없어 양식 목록도 못 본다.
	if _, err := e.forms().ListUsable(as(uAuditor)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사자: %v", err)
	}
}

// AC-111 필드 검증: 형식·범위·필수·선택지·표·첨부, 필드 키별 한국어 문구. 임시저장은 필수를 건너뛴다.
func TestValidateFieldsTable(t *testing.T) {
	e := newEnv(t)
	defs := expenseFields(t, e)
	good := `{"spend_date":"2026-11-10","spend_kind":"출장","payee":"김가나","items":[{"name":"택시","amount":15000}],"total":15000,"evidence":[1,2]}`
	if err := service.ValidateFields(defs, jsonVals(t, good), true); err != nil {
		t.Fatalf("정상 값이 거부됨: %v", err)
	}
	cases := []struct {
		name, vals, key, want string
		strict                bool
	}{
		{"필수 누락", `{}`, "spend_date", "「지출 일자」을(를) 입력하세요.", true},
		{"날짜 형식", `{"spend_date":"2026-13-40"}`, "spend_date", "「지출 일자」의 형식이 올바르지 않습니다.", false},
		{"선택지 밖", `{"spend_kind":"여행"}`, "spend_kind", "「지출 구분」은(는) 목록에서 선택하세요.", false},
		{"금액 음수", `{"total":-5}`, "total", "「금액」의 범위를 벗어났습니다.", false},
		{"금액 소수", `{"total":1.5}`, "total", "「금액」의 형식이 올바르지 않습니다.", false},
		{"글자 수", `{"payee":"` + strings.Repeat("가", 201) + `"}`, "payee", "「지급 대상」은(는) 200자 이내로 입력하세요.", false},
		{"표 행 부족", `{"items":[]}`, "items", "「지출 내역」을(를) 입력하세요.", true},
		{"표 열 누락", `{"items":[{"name":"택시"}]}`, "items[0].amount", "입력하세요", true},
		{"표 없는 열", `{"items":[{"name":"a","amount":1,"zzz":3}]}`, "items[0].zzz", "없는 열", false},
		{"첨부 개수", `{"evidence":[1,2,3,4,5,6,7,8,9,10,11]}`, "evidence", "10개까지", false},
		{"첨부 번호", `{"evidence":["x"]}`, "evidence", "형식이 올바르지 않습니다", false},
		{"없는 키", `{"hack":1}`, "hack", "이 양식에 없는 항목입니다", false},
	}
	for _, c := range cases {
		err := service.ValidateFields(defs, jsonVals(t, c.vals), c.strict)
		fe, ok := service.IsFieldErrors(err)
		if !ok || !errors.Is(err, contract.ErrValidation) {
			t.Fatalf("%s: 검증 오류가 아님: %v", c.name, err)
		}
		if msg := fe.ByKey()[c.key]; !strings.Contains(msg, c.want) {
			t.Fatalf("%s: 키 %q 문구 %q (기대 %q 포함), 전체 %v", c.name, c.key, msg, c.want, fe.ByKey())
		}
	}
	// 임시저장은 필수 누락을 허용하되 형식 오류는 거부한다.
	if err := service.ValidateFields(defs, jsonVals(t, `{}`), false); err != nil {
		t.Fatalf("임시저장에서 필수 누락이 거부됨: %v", err)
	}
	// 여러 오류는 키별로 모아 돌려준다.
	fe, _ := service.IsFieldErrors(service.ValidateFields(defs, jsonVals(t, `{}`), true))
	if len(fe.ByKey()) != 5 { // 지출 일자·구분·지급 대상·내역·금액·증빙 중 필수는 6개이나 사유만 선택 — 키는 6개
		if len(fe.ByKey()) != 6 {
			t.Fatalf("필수 오류 키 수 %d: %v", len(fe.ByKey()), fe.ByKey())
		}
	}
}

// AC-120·121 번호 규칙 렌더링과 연도 경계(KST), 규칙 검증.
func TestNumberingRenderAndYearBoundary(t *testing.T) {
	pat := "{unit}-{yyyy}-{seq:4}"
	kst := time.FixedZone("KST", 9*3600)
	at := func(y int, m time.Month, d, h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, kst) }
	n := domain.Numbering{Pattern: pat, AssignAt: "submit", Reset: "yearly"}
	if got := service.RenderNumber(pat, service.NumberParts{Unit: "구매팀", Now: at(2026, 12, 31, 23, 59, 59), Seq: 12}); got != "구매팀-2026-0012" {
		t.Fatal(got)
	}
	if y := service.SeqYear(n, at(2026, 12, 31, 23, 59, 59)); y != 2026 {
		t.Fatalf("연말 연도 %d", y)
	}
	if y := service.SeqYear(n, at(2027, 1, 1, 0, 0, 0)); y != 2027 {
		t.Fatalf("새해 연도 %d", y)
	}
	// UTC 로 주어져도 KST 날짜로 센다: 2026-12-31 15:00:00Z 는 KST 로 2027-01-01 00:00.
	if y := service.SeqYear(n, time.Date(2026, 12, 31, 15, 0, 0, 0, time.UTC)); y != 2027 {
		t.Fatalf("UTC 입력 연도 %d", y)
	}
	if y := service.SeqYear(domain.Numbering{Pattern: "A-{seq:3}", AssignAt: "submit", Reset: "never"}, at(2030, 1, 1, 0, 0, 0)); y != 0 {
		t.Fatalf("초기화 안 함 연도 %d", y)
	}
	if got := service.RenderNumber("PR-{id:6}", service.NumberParts{ID: 7}); got != "PR-000007" {
		t.Fatal(got)
	}
	if got := service.RenderNumber("{form}/{yy}/{unitcode}-{seq:2}", service.NumberParts{FormCode: "GEN", UnitCode: "BUY", Now: at(2026, 5, 1, 9, 0, 0), Seq: 3}); got != "GEN/26/BUY-03" {
		t.Fatal(got)
	}
	// 규칙 검증(AC-113).
	bad := map[string]domain.Numbering{
		"토큰":       {Pattern: "{zzz}-{seq:3}", AssignAt: "submit", Reset: "yearly"},
		"번호 없음":    {Pattern: "{yyyy}", AssignAt: "submit", Reset: "yearly"},
		"자릿수":      {Pattern: "{seq}", AssignAt: "submit", Reset: "yearly"},
		"시점":       {Pattern: "{seq:3}", AssignAt: "x", Reset: "yearly"},
		"작성 시 seq": {Pattern: "{seq:3}", AssignAt: "create", Reset: "yearly"},
		"중괄호":      {Pattern: "{seq:3}{", AssignAt: "submit", Reset: "yearly"},
	}
	for name, b := range bad {
		if len(service.ValidateNumbering(b)) == 0 {
			t.Fatalf("%s: 잘못된 규칙이 통과함", name)
		}
	}
	if p := service.ValidateNumbering(n); len(p) != 0 {
		t.Fatalf("정상 규칙이 거부됨: %v", p)
	}
}

// AC-123 일련번호는 같은 범위에서 이어지고 동시에 올려도 겹치지 않는다. 범위가 다르면(부서·연도) 따로 센다.
func TestCountersAreUniqueUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	repo := repository.SQLForms{}
	txm := contract.SQLTxManager{DB: e.db}
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := txm.WithTx(as(uStaff), func(tx contract.Tx) error {
				n, err := repo.NextSeq(as(uStaff), tx, 2, 1, 2026)
				mu.Lock()
				defer mu.Unlock()
				if seen[n] {
					t.Errorf("일련번호 %d 중복", n)
				}
				seen[n] = true
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i := 1; i <= 30; i++ {
		if !seen[i] {
			t.Fatalf("일련번호 %d 가 비었다(연속이어야 함)", i)
		}
	}
	// 다른 부서·다른 연도는 1부터 따로 센다. 롤백한 상신은 번호를 소모하지 않는다.
	for _, c := range []struct {
		unit int64
		year int
	}{{2, 2026}, {1, 2027}} {
		if err := txm.WithTx(as(uStaff), func(tx contract.Tx) error {
			n, err := repo.NextSeq(as(uStaff), tx, 2, c.unit, c.year)
			if n != 1 {
				t.Fatalf("범위 %+v 일련번호 %d", c, n)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	rollback := errors.New("롤백")
	_ = txm.WithTx(as(uStaff), func(tx contract.Tx) error {
		_, _ = repo.NextSeq(as(uStaff), tx, 2, 1, 2026)
		return rollback
	})
	_ = txm.WithTx(as(uStaff), func(tx contract.Tx) error {
		n, _ := repo.NextSeq(as(uStaff), tx, 2, 1, 2026)
		if n != 31 {
			t.Fatalf("롤백한 번호가 소모됨: %d", n)
		}
		return nil
	})
}

// AC-113 게시 전 검증: 필드 키 중복, 본문의 없는 필드, 번호 규칙, 금액 필드, 선택지 없음 등이 문구와 함께 게시를 거부한다.
func TestPublishValidation(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "GEN")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	def.Fields = append(def.Fields,
		domain.FieldDef{Key: "content", Label: "중복", Type: domain.FieldText},
		domain.FieldDef{Key: "pick", Label: "선택", Type: domain.FieldSelect},
		domain.FieldDef{Key: "tbl", Label: "표", Type: domain.FieldTable})
	def.Body = "내용 {{content}} {{nothing}}"
	def.Numbering.Pattern = "{zzz}"
	def.Settings.AmountField = "content"
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	_, err = f.Publish(as(uAdmin), v.ID)
	if !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("게시가 거부되어야 함: %v", err)
	}
	for _, want := range []string{"필드 키 「content」가 중복됩니다.", "본문 서식의 {{nothing}} 에 해당하는 필드가 없습니다.", "번호 규칙의 {zzz}을 알 수 없습니다.", "금액 필드여야 합니다", "선택지가 1~50개", "열이 1~10개"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("문구 %q 없음: %v", want, err)
		}
	}
	// 거부된 초안은 게시되지 않았다.
	got, _ := f.GetVersion(as(uAdmin), v.ID)
	if got.Status != "draft" {
		t.Fatalf("상태 %s", got.Status)
	}
	// 사용 부서 제한이 없는 조직을 가리키면 거부된다.
	def2 := v.Definition
	def2.DeptLimit = []int64{9999}
	_, _ = f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def2})
	if _, err := f.Publish(as(uAdmin), v.ID); err == nil || !strings.Contains(err.Error(), "조직(번호 9999)이 없습니다") {
		t.Fatalf("없는 조직: %v", err)
	}
}

// AC-112·115 게시·새 버전·퇴역·불변성, 권한(form:manage 만), 감사. 새 기안은 최신 게시 버전을 쓰고 기존 버전은 그대로 남는다.
func TestFormVersionLifecycleAndPermission(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	// 관리 권한이 없으면 403 이고 거부가 감사에 남는다.
	if _, err := f.List(as(uStaff)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원의 양식 관리: %v", err)
	}
	if !e.rec.Has("approval_form.list", "approval_form", "denied") {
		t.Fatal("거부 감사 기록 없음")
	}
	if _, err := f.CreateVersionDraft(as(uStaff), "GEN"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("직원이 새 버전을 만듦")
	}
	v, err := f.CreateVersionDraft(as(uAdmin), "GEN")
	if err != nil || v.Version != 2 || v.Status != "draft" {
		t.Fatalf("새 버전: %+v %v", v, err)
	}
	// 초안이 있으면 새로 만들지 않고 그 초안을 돌려준다.
	if again, err := f.CreateVersionDraft(as(uAdmin), "GEN"); err != nil || again.ID != v.ID {
		t.Fatalf("초안 중복: %+v %v", again, err)
	}
	def := v.Definition
	def.Fields = append(def.Fields, domain.FieldDef{Key: "memo", Label: "메모", Type: domain.FieldText})
	def.RetentionYears = 10
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	// 게시 전: 사용자는 여전히 버전 1.
	if d, _ := f.Get(as(uStaff), "GEN"); d.Version != 1 || len(d.Definition.Fields) != 3 {
		t.Fatalf("게시 전 기안 화면: %+v", d.FormSummary)
	}
	pub, err := f.Publish(as(uAdmin), v.ID)
	if err != nil || pub.Status != "published" {
		t.Fatalf("게시: %+v %v", pub, err)
	}
	d, _ := f.Get(as(uStaff), "GEN")
	if d.Version != 2 || len(d.Definition.Fields) != 4 || d.Definition.RetentionYears != 10 {
		t.Fatalf("게시 뒤 최신 버전: %+v", d.FormSummary)
	}
	// 이전 버전은 퇴역하되 정의가 그대로다(기존 문서가 당시 버전으로 표시된다).
	old, _ := f.GetVersion(as(uAdmin), 2) // 시드 GEN 버전 1 의 id 는 2
	if old.Status != "retired" || len(old.Definition.Fields) != 3 {
		t.Fatalf("이전 버전: %+v", old)
	}
	// 게시된 버전은 저장할 수 없다.
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("게시된 버전 저장: %v", err)
	}
	for _, a := range []string{"approval_form.create", "approval_form.draft_save", "approval_form.publish"} {
		if !e.rec.Has(a, "approval_form", "success") {
			t.Fatalf("감사 %s 없음", a)
		}
	}
	// 사용 중지하면 새 기안 목록에서 빠진다(관리 목록에는 남는다).
	if err := f.SetActive(as(uAdmin), "GEN", false); err != nil {
		t.Fatal(err)
	}
	list, _ := f.ListUsable(as(uStaff))
	for _, s := range list {
		if s.Code == "GEN" {
			t.Fatal("사용 중지된 양식이 기안 목록에 보임")
		}
	}
	rows, _ := f.List(as(uAdmin))
	found := false
	for _, r := range rows {
		found = found || r.Code == "GEN" && !r.Active && r.LatestVersion == 2
	}
	if !found {
		t.Fatalf("관리 목록: %+v", rows)
	}
}

// 시스템 양식(구매 품의서)은 필드·본문을 바꿀 수 없지만 번호·보존·설정은 바꿀 수 있다.
func TestSystemFormFieldsAreFixed(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, err := f.CreateVersionDraft(as(uAdmin), "PUR")
	if err != nil {
		t.Fatal(err)
	}
	def := v.Definition
	def.Fields = []domain.FieldDef{{Key: "x", Label: "x", Type: domain.FieldText}}
	def.Body = "{{x}}"
	def.RetentionYears = 10
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "시스템 양식의 필드와 본문은 고칠 수 없습니다") {
		t.Fatalf("시스템 양식의 필드 변경이 조용히 무시되거나 통과함: %v", err)
	}
	def.Fields, def.Body = nil, ""
	saved, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Definition.Fields) != 0 || saved.Definition.Body != "" || saved.Definition.RetentionYears != 10 || !saved.Definition.System {
		t.Fatalf("시스템 양식 저장: %+v", saved.Definition)
	}
}

// AC-114 사용 부서 제한: 제한 밖 소속은 거부, 안(하위 조직 포함)은 통과, 기안 화면 목록에도 제한을 반영한다.
func TestDeptLimit(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "COOP")
	def := v.Definition
	def.DeptLimit = []int64{unitBuy} // 구매팀(부서)과 그 하위(구매1팀)
	if _, err := f.SaveVersionDraft(as(uAdmin), v.ID, domain.FormVersionInput{Definition: def}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(as(uAdmin), v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(as(uStaff), "COOP"); err != nil { // 구매1팀 직원: 하위 조직
		t.Fatalf("제한 안(하위 조직): %v", err)
	}
	_, err := f.Get(as(uStaff2), "COOP") // 영업1팀 직원
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "이 양식은 구매팀 소속만 쓸 수 있습니다.") {
		t.Fatalf("제한 밖: %v", err)
	}
	list, _ := f.ListUsable(as(uStaff2))
	for _, s := range list {
		if s.Code == "COOP" {
			t.Fatal("제한 밖 사용자의 목록에 양식이 보임")
		}
	}
}

// 번호 규칙 미리보기: 호출자 부서 이름과 다음 일련번호를 보인다(올리지 않는다).
func TestPreviewNumber(t *testing.T) {
	e := newEnv(t)
	f := e.forms()
	v, _ := f.CreateVersionDraft(as(uAdmin), "GEN")
	got, err := f.PreviewNumber(as(uAdmin), v.ID)
	if err != nil || got != "재무팀-2026-0001" {
		t.Fatalf("미리보기 %q %v", got, err)
	}
	again, _ := f.PreviewNumber(as(uAdmin), v.ID)
	if again != got {
		t.Fatal("미리보기가 일련번호를 올림")
	}
}
