package evalset_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

type env struct {
	t   *testing.T
	db  *sql.DB
	rec *fake.Recorder
	svc *evalset.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (900, '운영')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'admin@pl.test', '관리자', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	rec := fake.NewRecorder()
	svc, err := evalset.New(evalset.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: rec, Masker: rbac.Masker{}, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, db: db, rec: rec, svc: svc}
}

func as(perms ...contract.Permission) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(perms...)})
}

var (
	admin  = as(contract.PermEvalRead, contract.PermEvalWrite, contract.PermEvalRun)
	reader = as(contract.PermEvalRead)
)

func (e *env) newSet(code, purpose string) {
	e.t.Helper()
	if _, err := e.svc.CreateSet(admin, evalset.SetInput{Code: code, Name: "시험 세트", Purpose: purpose}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) text(code, q, exp, grader string) evalset.SaveResult {
	e.t.Helper()
	r, err := e.svc.SaveItem(admin, code, evalset.ItemInput{Question: q, Expected: exp, Grader: grader})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// AC-4601: 초안 편집 → 게시 후 불변(서비스·트리거), 새 초안은 직전 버전을 복사, 문항 0 이면 게시 거부.
func TestDraftPublishImmutableAndNewDraftCopies(t *testing.T) {
	e := newEnv(t)
	e.newSet("faq", evalset.PurposeText)
	if _, err := e.svc.Publish(admin, "faq", ""); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "문항이 없어") {
		t.Fatalf("문항 0 게시: %v", err)
	}
	a := e.text("faq", "휴가 신청은 어떻게 하나요?", "결재 문서로 신청", evalset.GraderContains)
	e.text("faq", "연차는 몇 일?", "15", evalset.GraderNumber)
	d, err := e.svc.Publish(admin, "faq", "첫 게시")
	if err != nil || d.CurrentVersion != 1 || d.Selected.Status != "published" || d.Selected.ItemsHash == "" || len(d.Items) != 2 {
		t.Fatalf("게시: %+v %v", d, err)
	}
	// 게시된 버전은 서비스와 DB 트리거 모두 거부한다.
	if _, err := e.db.Exec(`UPDATE eval_items SET question = 'x' WHERE id = ?`, a.Item.ID); err == nil || !strings.Contains(err.Error(), "게시된 버전은 고칠 수 없습니다") {
		t.Fatalf("트리거(수정): %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM eval_items WHERE id = ?`, a.Item.ID); err == nil {
		t.Fatal("트리거(삭제)")
	}
	if _, err := e.db.Exec(`UPDATE eval_set_versions SET note = 'x' WHERE version = 1`); err == nil {
		t.Fatal("트리거(버전 수정)")
	}
	if _, err := e.svc.SaveItem(admin, "faq", evalset.ItemInput{Question: "q", Expected: "a", Grader: evalset.GraderExact}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "초안이 없습니다") {
		t.Fatalf("초안 없이 문항 추가: %v", err)
	}
	// 새 초안은 직전 게시 버전의 문항을 복사하고 게시하면 버전이 오른다. 같은 내용이면 해시가 같다.
	d2, err := e.svc.NewDraft(admin, "faq")
	if err != nil || d2.Selected.Version != 2 || len(d2.Items) != 2 {
		t.Fatalf("새 초안: %+v %v", d2, err)
	}
	if _, err := e.svc.NewDraft(admin, "faq"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("초안이 이미 있으면 거부: %v", err)
	}
	e.text("faq", "새 문항", "새 답", evalset.GraderExact)
	d3, err := e.svc.Publish(admin, "faq", "")
	if err != nil || d3.CurrentVersion != 2 || len(d3.Items) != 3 {
		t.Fatalf("2차 게시: %+v %v", d3, err)
	}
	if d3.Versions[1].ItemsHash != d.Selected.ItemsHash {
		t.Fatalf("1버전 내용은 그대로여야 한다")
	}
	if d3.Selected.ItemsHash == d.Selected.ItemsHash {
		t.Fatal("문항이 다르면 해시가 달라야 한다")
	}
}

// 용도별 검증과 채점 기준 규칙, 낙관적 잠금.
func TestItemValidationPerPurposeAndRevision(t *testing.T) {
	e := newEnv(t)
	e.newSet("docs", evalset.PurposeDoc)
	e.newSet("pol", evalset.PurposePolicy)
	e.newSet("tools", evalset.PurposeTool)
	e.newSet("txt", evalset.PurposeText)
	bad := func(code string, in evalset.ItemInput, want string) {
		t.Helper()
		if _, err := e.svc.SaveItem(admin, code, in); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s %+v: 거부돼야 한다(%q): %v", code, in, want, err)
		}
	}
	bad("docs", evalset.ItemInput{Question: "문서", Expected: "공급가액 1000", Grader: evalset.GraderExact}, "JSON 객체")
	bad("docs", evalset.ItemInput{Question: "문서", Expected: `{"공급가액":1000}`, Grader: evalset.GraderContains}, "정확 일치 또는 숫자")
	bad("pol", evalset.ItemInput{Question: "질의", Expected: "3.2조", Grader: evalset.GraderExact}, "포함 채점")
	bad("tools", evalset.ItemInput{Question: "요청", Expected: `{"args":{}}`, Grader: evalset.GraderExact}, `"tool"`)
	bad("txt", evalset.ItemInput{Question: "q", Expected: "a", Grader: "regex"}, "채점 기준을 고르세요")
	bad("txt", evalset.ItemInput{Question: "q", Expected: "10", Grader: evalset.GraderNumber, Tolerance: "foo"}, "허용 오차")
	bad("txt", evalset.ItemInput{Question: "q", Expected: "10", Grader: evalset.GraderNumber, Tolerance: "rel:150"}, "허용 오차")
	bad("txt", evalset.ItemInput{Question: "q", Expected: "a", Grader: evalset.GraderLLM, Rubric: "그냥 잘 쓰면 됨"}, "필수:")
	bad("txt", evalset.ItemInput{Question: "q", Expected: "a", Grader: evalset.GraderExact, Context: "[1,2]"}, "JSON 객체")
	bad("txt", evalset.ItemInput{Question: "", Expected: "a", Grader: evalset.GraderExact}, "질문")
	ok := []struct {
		code string
		in   evalset.ItemInput
	}{
		{"docs", evalset.ItemInput{Question: "문서", Expected: `{"공급가액":1000000}`, Grader: evalset.GraderNumber, Tolerance: "rel:1"}},
		{"pol", evalset.ItemInput{Question: "금액 구간별 결재선", Expected: "3.2조\n3.4조", Grader: evalset.GraderContains, Context: `{"role":"staff"}`}},
		{"tools", evalset.ItemInput{Question: "용지 재고", Expected: `{"tool":"search_items","args":{"query":"용지"}}`, Grader: evalset.GraderExact, Critical: true}},
		{"txt", evalset.ItemInput{Question: "q", Expected: "a", Grader: evalset.GraderLLM, Rubric: "필수: 신청\n금지: 거절"}},
	}
	for _, c := range ok {
		if _, err := e.svc.SaveItem(admin, c.code, c.in); err != nil {
			t.Errorf("%s: %v", c.code, err)
		}
	}
	// 숫자 채점은 허용 오차 기본값이 abs:0 이다.
	r := e.text("txt", "n", "5", evalset.GraderNumber)
	if r.Item.Tolerance != "abs:0" {
		t.Fatalf("기본 허용 오차: %q", r.Item.Tolerance)
	}
	// 낙관적 잠금: 오래된 수정 번호는 거부된다.
	if _, err := e.svc.SaveItem(admin, "txt", evalset.ItemInput{Question: "q2", Expected: "a2", Grader: evalset.GraderExact, Revision: 1}); !errors.Is(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "다른 사람이 먼저") {
		t.Fatalf("오래된 수정 번호: %v", err)
	}
	if _, err := e.svc.DeleteItem(admin, "txt", r.Item.ID, 1); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("오래된 수정 번호로 삭제: %v", err)
	}
	rev, err := e.svc.DeleteItem(admin, "txt", r.Item.ID, r.Revision)
	if err != nil || rev != r.Revision+1 {
		t.Fatalf("삭제: %d %v", rev, err)
	}
}

// AC-4603: 문항의 사업자번호·계좌·연락처는 저장 전에 가려지고 개수가 기록되며 DB·내보내기·감사에 원문이 없다.
func TestItemsAreMaskedBeforeStorage(t *testing.T) {
	e := newEnv(t)
	e.newSet("mask", evalset.PurposeText)
	r, err := e.svc.SaveItem(admin, "mask", evalset.ItemInput{Question: "거래처 사업자번호 123-45-67890 에 연락처 010-1234-5678 로 전화해도 되나요?", Expected: "사업자번호 123-45-67890 는 가려야 합니다", Grader: evalset.GraderContains, Tags: "a@pl.test"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Item.MaskedCount == 0 || len(r.Warnings) < 2 || !strings.Contains(strings.Join(r.Warnings, " "), "가려진 값이 기대 답에 들어") {
		t.Fatalf("가림 안내: %+v", r)
	}
	for _, leak := range []string{"123-45-67890", "010-1234-5678"} {
		var n int
		_ = e.db.QueryRow(`SELECT COUNT(*) FROM eval_items WHERE question LIKE ? OR expected LIKE ? OR tags LIKE ?`, "%"+leak+"%", "%"+leak+"%", "%"+leak+"%").Scan(&n)
		if n != 0 {
			t.Fatalf("DB 에 원문 %s 가 있다", leak)
		}
	}
	if _, err := e.svc.Publish(admin, "mask", ""); err != nil {
		t.Fatal(err)
	}
	data, _, err := e.svc.ExportCSV(admin, "mask", 0)
	if err != nil || strings.Contains(string(data), "123-45-67890") || strings.Contains(string(data), "010-1234-5678") {
		t.Fatalf("내보내기에 원문: %v %s", err, data)
	}
	for _, rec := range e.rec.Records() {
		if strings.Contains(rec.Detail, "123-45") {
			t.Fatalf("감사에 원문: %+v", rec)
		}
		for _, d := range rec.Diff {
			if strings.Contains(d.New, "123-45") || strings.Contains(d.Old, "123-45") {
				t.Fatalf("감사 diff 에 원문: %+v", d)
			}
		}
	}
}

// AC-4604: CSV 형식 오류는 행·열만 알리고 전체를 취소, 수식은 내보내기에서 무력화되고 왕복이 문항을 보존한다.
func TestCSVImportExportRoundTripAndErrors(t *testing.T) {
	e := newEnv(t)
	e.newSet("csv", evalset.PurposeText)
	good := "\xEF\xBB\xBF문항,기대,채점,허용오차,치명,태그,맥락,기준\n" +
		"휴가는 어떻게 신청하나요?,결재 문서로 신청,포함,,예,hr,,\n" +
		"=SUM(A1) 은?,10,숫자,abs:0.5,아니오,,,\n" +
		"설명해 주세요,승인,LLM,,아니오,,,\"필수: 승인\n금지: 거절\"\n"
	res, err := e.svc.ImportCSV(admin, "csv", []byte(good))
	if err != nil || res.Imported != 3 || res.DraftVersion != 1 {
		t.Fatalf("가져오기: %+v %v", res, err)
	}
	if _, err := e.svc.Publish(admin, "csv", ""); err != nil {
		t.Fatal(err)
	}
	data, name, err := e.svc.ExportCSV(admin, "csv", 0)
	if err != nil || name != "csv-v1.csv" {
		t.Fatalf("내보내기: %v %s", err, name)
	}
	if !strings.Contains(string(data), "'=SUM(A1) 은?") {
		t.Fatalf("수식 칸은 작은따옴표로 무력화돼야 한다:\n%s", data)
	}
	// 왕복: 내보낸 파일을 다른 세트에 가져오면 같은 문항(해시 동일)이 된다.
	e.newSet("csv2", evalset.PurposeText)
	if _, err := e.svc.ImportCSV(admin, "csv2", data); err != nil {
		t.Fatalf("왕복 가져오기: %v", err)
	}
	d1, _ := e.svc.Get(admin, "csv", 1)
	if _, err := e.svc.Publish(admin, "csv2", ""); err != nil {
		t.Fatal(err)
	}
	d2, _ := e.svc.Get(admin, "csv2", 1)
	if d1.Selected.ItemsHash != d2.Selected.ItemsHash || d2.Items[1].Question != "=SUM(A1) 은?" {
		t.Fatalf("왕복이 문항을 보존하지 못했다: %s %s %q", d1.Selected.ItemsHash, d2.Selected.ItemsHash, d2.Items[1].Question)
	}
	// 오류: 행·열만 알리고(값 없음) 한 행이라도 틀리면 전부 취소한다.
	e.newSet("csv3", evalset.PurposeText)
	badCSV := "문항,기대,채점,허용오차,치명,태그,맥락,기준\n정상 문항,정상 답,정확,,,,,\n비밀 문항,x,정규식,,,,,\n"
	_, err = e.svc.ImportCSV(admin, "csv3", []byte(badCSV))
	if !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "3행 3열(채점)") || strings.Contains(err.Error(), "비밀 문항") {
		t.Fatalf("오류 문장: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM eval_items i JOIN eval_set_versions v ON v.id = i.version_id JOIN eval_sets s ON s.id = v.set_id WHERE s.code = 'csv3'`); n != 0 {
		t.Fatalf("일부만 들어갔다: %d", n)
	}
	if _, err := e.svc.ImportCSV(admin, "csv3", []byte("이상한,머리글\n")); !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), "머리글") {
		t.Fatalf("머리글: %v", err)
	}
	// 게시된 세트에 가져오면 직전 버전을 복사한 새 초안에 더해진다.
	res2, err := e.svc.ImportCSV(admin, "csv", []byte("문항,기대,채점\n새 문항,새 답,정확\n"))
	if err != nil || res2.DraftVersion != 2 {
		t.Fatalf("게시된 세트에 가져오기: %+v %v", res2, err)
	}
	d, _ := e.svc.Get(admin, "csv", 0)
	if len(d.Items) != 4 || d.Selected.Status != "draft" {
		t.Fatalf("복사 후 추가: %d %s", len(d.Items), d.Selected.Status)
	}
}

func (e *env) count(q string) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// AC-4608·4609: 권한(읽기만 있으면 못 고친다)과 감사(값 없이, 쓰기와 같은 트랜잭션), 세트 코드·상한 검증.
func TestPermissionsAuditAndSetValidation(t *testing.T) {
	e := newEnv(t)
	e.newSet("perm", evalset.PurposeText)
	e.text("perm", "q", "a", evalset.GraderExact)
	if _, err := e.svc.ListSets(reader); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(reader, "perm", 0); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() error{
		"만들기": func() error {
			_, err := e.svc.CreateSet(reader, evalset.SetInput{Code: "x1", Name: "x", Purpose: "text"})
			return err
		},
		"문항": func() error {
			_, err := e.svc.SaveItem(reader, "perm", evalset.ItemInput{Question: "q", Expected: "a", Grader: "exact"})
			return err
		},
		"게시": func() error { _, err := e.svc.Publish(reader, "perm", ""); return err },
		"가져오기": func() error {
			_, err := e.svc.ImportCSV(reader, "perm", []byte("문항,기대,채점\nq,a,정확\n"))
			return err
		},
	} {
		if err := fn(); !errors.Is(err, contract.ErrForbidden) {
			t.Errorf("%s: 읽기 권한만으로는 거부돼야 한다: %v", name, err)
		}
	}
	if _, err := e.svc.ListSets(as()); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("권한 없는 목록: %v", err)
	}
	for _, bad := range []string{"A", "bad code", "x", strings.Repeat("a", 31)} {
		if _, err := e.svc.CreateSet(admin, evalset.SetInput{Code: bad, Name: "x", Purpose: "text"}); !errors.Is(err, contract.ErrValidation) {
			t.Errorf("코드 %q: %v", bad, err)
		}
	}
	if _, err := e.svc.CreateSet(admin, evalset.SetInput{Code: "perm", Name: "또", Purpose: "text"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("코드 중복: %v", err)
	}
	if _, err := e.svc.CreateSet(admin, evalset.SetInput{Code: "okcode", Name: "x", Purpose: "qa"}); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("용도: %v", err)
	}
	if _, err := e.svc.Publish(admin, "perm", "첫"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ExportCSV(reader, "perm", 0); err != nil {
		t.Fatal(err)
	}
	f := false
	if _, err := e.svc.UpdateSet(admin, "perm", evalset.SetInput{Name: "새 이름", Active: &f}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.NewDraft(admin, "perm"); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("꺼진 세트에 새 초안: %v", err)
	}
	seen := map[string]bool{}
	for _, rec := range e.rec.Records() {
		seen[rec.Action] = true
	}
	for _, a := range []string{"eval.set_save", "eval.version_publish", "eval.items_export"} {
		if !seen[a] {
			t.Errorf("감사 %s 가 없다", a)
		}
	}
}
