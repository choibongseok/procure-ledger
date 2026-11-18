package importflow

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	cat "github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/importx"
	"github.com/choibongseok/procure-ledger/migrations"
)

// fakeHandler 는 창고 대상의 가짜 어댑터다. 별도 시험 표 fake_wh(코드, 이름)를 키로 비교하고 같은 트랜잭션으로 쓴다.
type fakeHandler struct {
	failAfter int // 이 건수만큼 쓴 뒤 실패한다(0 이면 실패 없음)
	sawBatch  string
	applied   int
}

func (h *fakeHandler) Target() string { return domain.ImportWarehouse }

func (h *fakeHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var p domain.ImportPlan
	for _, r := range rows {
		var name string
		err := q.QueryRowContext(ctx, `SELECT name FROM fake_wh WHERE code = ?`, r.Key).Scan(&name)
		v := domain.ImportRowVerdict{RowNo: r.RowNo, Key: r.Cells["code"]}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			v.Verdict = domain.ImportVerdictAdd
		case err != nil:
			return p, err
		case name == r.Cells["name"]:
			v.Verdict = domain.ImportVerdictSame
		default:
			v.Verdict = domain.ImportVerdictChange
			v.Changes = []domain.ImportChange{{Column: "이름", Old: name, New: r.Cells["name"]}}
		}
		if strings.Contains(r.Cells["name"], "금지") {
			v.Verdict, v.Reason = domain.ImportVerdictError, "사용할 수 없는 이름입니다."
		}
		if r.Cells["location"] == "" && v.Verdict != domain.ImportVerdictError {
			v.Warn = "위치가 비어 있습니다."
		}
		p.Verdicts = append(p.Verdicts, v)
	}
	p.Summary = []string{"창고 " + itoa(len(rows)) + "곳"}
	return p, nil
}

func (h *fakeHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	h.sawBatch = in.BatchNo
	for i, r := range rows {
		if h.failAfter > 0 && i >= h.failAfter {
			return domain.ImportApplyOutcome{}, errors.New("시험용 실패")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fake_wh(code, name) VALUES(?, ?) ON CONFLICT(code) DO UPDATE SET name = excluded.name`, r.Key, r.Cells["name"]); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		h.applied++
	}
	return domain.ImportApplyOutcome{JournalNos: []string{"JV-시험-1"}}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

type failAudit struct {
	cat.Recorder
	fail bool
}

func (f *failAudit) Record(ctx context.Context, tx contract.Tx, e cat.Entry) error {
	if f.fail {
		return contract.ErrAuditFailed
	}
	return f.Recorder.Record(ctx, tx, e)
}

type env struct {
	db      *sql.DB
	svc     *Service
	h       *fakeHandler
	aud     *failAudit
	clock   *time.Time
	admin   contract.Principal // import:master·read
	finance contract.Principal // 세 권한 모두
	auditor contract.Principal // import:read
	staff   contract.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (1, '구매팀')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE fake_wh(code TEXT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := access.SyncTemplates(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	e := &env{db: db, clock: &now, h: &fakeHandler{}}
	e.aud = &failAudit{Recorder: audit.New(db)}
	e.svc = New(Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: e.aud, Now: func() time.Time { return *e.clock }})
	e.svc.RegisterTarget(e.h)
	mk := func(email, role string) contract.Principal {
		id := dbtest.InsertID(t, db, `INSERT INTO users (email, name, role, department_id) VALUES (?,?,?,1)`, email, email, role)
		perms, _ := access.PermsOf(ctx, db, id)
		return contract.Principal{UserID: id, Name: email, Role: contract.Role(role), Perms: perms, Channel: contract.ChannelWeb, Auth: contract.AuthSession}
	}
	e.admin, e.finance, e.auditor, e.staff = mk("a@x", "admin"), mk("f@x", "finance"), mk("u@x", "auditor"), mk("s@x", "staff")
	return e
}

func as(p contract.Principal) context.Context { return contract.WithPrincipal(context.Background(), p) }

// makeFile 은 창고 양식 파일을 만든다(양식 정의의 머리글·버전 그대로, B28 쓰기 부품).
func makeFile(t *testing.T, rows [][]string) []byte {
	t.Helper()
	tg := importx.Targets[domain.ImportWarehouse]
	cols := make([]export.Column, len(tg.Columns))
	for i, c := range tg.Columns {
		cols[i] = export.Column{Label: c.Header(), Kind: export.KindText}
	}
	var cells [][]export.Cell
	for _, r := range rows {
		row := make([]export.Cell, len(cols))
		for i := range cols {
			if i < len(r) && r[i] != "" {
				row[i] = r[i]
			}
		}
		cells = append(cells, row)
	}
	data, err := export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: tg.Code, Label: tg.Label, Columns: cols}, Rows: cells}, export.Meta{At: time.Now(), By: "시험", Filters: "양식 버전 1"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (e *env) n(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func isErr(err, target error) bool { return errors.Is(err, target) }

func TestPreviewApplyHistoryFlow(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "본창고", "이천"}, {"WH-02", "보조창고", "이천"}})
	pv, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Counts.Add != 2 || pv.Counts.Err != 0 || !pv.Applicable || pv.Token == "" || pv.BatchNo != "IMP-2026-0001" || len(pv.Summary) != 1 || pv.KeyRule == "" {
		t.Fatalf("미리보기: %+v", pv)
	}
	// 미리보기는 업무 데이터를 바꾸지 않고 감사를 남기지 않는다.
	if e.n(t, `SELECT COUNT(*) FROM fake_wh`) != 0 || e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'import.%'`) != 0 {
		t.Fatal("미리보기가 데이터를 바꾸거나 감사를 남김")
	}
	// 반영 토큰·파일 검사.
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, "틀린값", file); !isErr(err, contract.ErrValidation) {
		t.Fatalf("틀린 토큰: %v", err)
	}
	other := makeFile(t, [][]string{{"WH-01", "다른 이름", "이천"}})
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, other); !isErr(err, contract.ErrValidation) {
		t.Fatalf("다른 파일: %v", err)
	}
	if _, err := e.svc.Apply(as(e.finance), domain.ImportWarehouse, pv.BatchNo, pv.Token, file); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("미리보기한 사람이 아닌 반영: %v", err)
	}
	b, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file)
	if err != nil || b.Status != domain.ImportApplied || b.Counts.Add != 2 || len(b.JournalNos) != 1 {
		t.Fatalf("반영: %+v %v", b, err)
	}
	if e.h.sawBatch != pv.BatchNo || e.n(t, `SELECT COUNT(*) FROM fake_wh`) != 2 {
		t.Fatalf("어댑터 반영: %q", e.h.sawBatch)
	}
	if n := e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='import.apply' AND target_id=?`, pv.BatchNo); n != 1 {
		t.Fatalf("반영 감사 %d건", n)
	}
	// 일회용 토큰.
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("재반영: %v", err)
	}
	// 같은 파일 재업로드: 전부 동일, 묶음 없음, 이미 반영한 파일 안내.
	again, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if err != nil || again.BatchNo != "" || again.Token != "" || again.Counts.Same != 2 || again.Applicable || again.SameFileBatch != pv.BatchNo || !strings.Contains(again.Message, "이미 반영한 파일") {
		t.Fatalf("재업로드: %+v %v", again, err)
	}
	// 일부만 바뀐 파일은 바뀐 행만 반영한다.
	part := makeFile(t, [][]string{{"WH-01", "본창고", "이천"}, {"WH-02", "새 이름", "이천"}, {"WH-03", "셋째", ""}})
	pp, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", part)
	if err != nil || pp.Counts.Same != 1 || pp.Counts.Chg != 1 || pp.Counts.Add != 1 || pp.Counts.Warn != 1 || pp.BatchNo != "IMP-2026-0002" {
		t.Fatalf("일부 변경: %+v %v", pp, err)
	}
	e.h.applied = 0
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pp.BatchNo, pp.Token, part); err != nil || e.h.applied != 2 {
		t.Fatalf("바뀐 행만 반영: %d %v", e.h.applied, err)
	}
	// 이력: 감사·조회 권한으로 보이고 직원은 못 본다.
	hist, err := e.svc.History(as(e.auditor), domain.ImportFilter{})
	if err != nil || len(hist) != 2 || hist[0].No != "IMP-2026-0002" {
		t.Fatalf("이력: %+v %v", hist, err)
	}
	if _, err := e.svc.History(as(e.staff), domain.ImportFilter{}); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원의 이력: %v", err)
	}
	d, err := e.svc.Batch(as(e.auditor), pp.BatchNo)
	if err != nil || len(d.Rows) != 3 || d.Rows[0].Warn == "" {
		t.Fatalf("상세(경고 행이 먼저): %+v %v", d, err)
	}
}

func TestErrorRowsBlockApplyAndErrorFile(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "정상", "이천"}, {"!", "코드오류", ""}, {"WH-03", "금지어 이름", "이천"}})
	pv, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Counts.Err != 2 || pv.Applicable || pv.Token != "" || pv.BatchNo == "" || !strings.Contains(pv.Message, "오류 행 2건") {
		t.Fatalf("오류 미리보기: %+v", pv)
	}
	if pv.Rows[0].Verdict != domain.ImportVerdictError {
		t.Fatalf("오류 행이 먼저 보여야 함: %+v", pv.Rows)
	}
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, "", file); !isErr(err, contract.ErrValidation) {
		t.Fatalf("토큰 없는 반영: %v", err)
	}
	f, err := e.svc.ErrorFile(as(e.admin), pv.BatchNo)
	if err != nil || len(f.Data) == 0 || !strings.HasSuffix(f.Name, ".xlsx") {
		t.Fatalf("오류 파일: %v", err)
	}
	if _, err := e.svc.ErrorFile(as(e.staff), pv.BatchNo); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("직원의 오류 파일: %v", err)
	}
	if _, err := e.svc.ErrorFile(as(e.auditor), pv.BatchNo); err != nil {
		t.Fatalf("감사의 오류 파일: %v", err)
	}
}

func TestApplyIsOneTransaction(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "하나", "a"}, {"WH-02", "둘", "a"}, {"WH-03", "셋", "a"}})
	pv, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	// 어댑터가 중간에 실패하면 먼저 쓴 행도 남지 않는다.
	e.h.failAfter = 2
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file); err == nil {
		t.Fatal("어댑터 실패인데 성공")
	}
	// 감사가 실패해도 취소된다.
	e.h.failAfter = 0
	e.aud.fail = true
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file); !isErr(err, contract.ErrAuditFailed) {
		t.Fatalf("감사 실패: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM fake_wh`) != 0 || e.n(t, `SELECT COUNT(*) FROM import_batches WHERE status='applied'`) != 0 {
		t.Fatal("실패한 반영이 일부 남음")
	}
	// 실패 뒤에도 같은 토큰으로 다시 반영할 수 있다(미리보기 상태 유지).
	e.aud.fail = false
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file); err != nil {
		t.Fatalf("복구 뒤 반영: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM fake_wh`) != 3 {
		t.Fatal("반영 건수")
	}
}

func TestDataChangedSincePreviewRejectsApply(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "본창고", "a"}})
	pv, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if _, err := e.db.Exec(`INSERT INTO fake_wh VALUES ('wh-01', '다른 사람이 먼저 만듦')`); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, pv.BatchNo, pv.Token, file)
	if !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "미리보기 이후 데이터가 바뀌었습니다") {
		t.Fatalf("E9: %v", err)
	}
}

func TestStateTransitions(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "본창고", "a"}})
	// 새 미리보기는 같은 사람의 이전 미리보기를 만료시킨다.
	p1, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	p2, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, p1.BatchNo, p1.Token, file); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "만료") {
		t.Fatalf("대체된 미리보기: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM import_rows r JOIN import_batches b ON b.id=r.batch_id WHERE b.no=?`, p1.BatchNo) != 0 {
		t.Fatal("만료된 미리보기의 임시 행이 남음")
	}
	// 취소: 요청자만, 취소 뒤에는 반영·재취소 불가, 감사 1건.
	if err := e.svc.Cancel(as(e.finance), domain.ImportWarehouse, p2.BatchNo); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("남의 취소: %v", err)
	}
	if err := e.svc.Cancel(as(e.admin), domain.ImportWarehouse, p2.BatchNo); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(as(e.admin), domain.ImportWarehouse, p2.BatchNo); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("재취소: %v", err)
	}
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, p2.BatchNo, p2.Token, file); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("취소 뒤 반영: %v", err)
	}
	if e.n(t, `SELECT COUNT(*) FROM audit_logs WHERE action='import.cancel'`) != 1 {
		t.Fatal("취소 감사")
	}
	// 만료: 24시간이 지난 미리보기.
	p3, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	*e.clock = e.clock.Add(25 * time.Hour)
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, p3.BatchNo, p3.Token, file); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "만료") {
		t.Fatalf("만료 뒤 반영: %v", err)
	}
	// 반영된 묶음은 끝 상태다(DB 도 고칠 수 없다).
	*e.clock = e.clock.Add(-25 * time.Hour)
	p4, _ := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
	if _, err := e.svc.Apply(as(e.admin), domain.ImportWarehouse, p4.BatchNo, p4.Token, file); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE import_batches SET status='canceled' WHERE no=?`, p4.BatchNo); err == nil {
		t.Error("반영된 묶음의 상태를 바꿀 수 있음")
	}
	if _, err := e.db.Exec(`DELETE FROM import_rows WHERE batch_id=(SELECT id FROM import_batches WHERE no=?)`, p4.BatchNo); err == nil {
		t.Error("반영된 묶음의 행을 지울 수 있음")
	}
	if _, err := e.db.Exec(`UPDATE import_rows SET reason='x'`); err == nil {
		t.Error("행 수정이 막히지 않음")
	}
	if _, err := e.db.Exec(`DELETE FROM import_batches`); err == nil {
		t.Error("묶음 삭제가 막히지 않음")
	}
}

func TestPermissionsAndLimits(t *testing.T) {
	e := newEnv(t)
	file := makeFile(t, [][]string{{"WH-01", "본창고", "a"}})
	for _, who := range []contract.Principal{e.staff, e.auditor} {
		if _, err := e.svc.Preview(as(who), domain.ImportWarehouse, "", file); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%s 미리보기: %v", who.Name, err)
		}
		if _, err := e.svc.Template(as(who), domain.ImportWarehouse); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%s 양식: %v", who.Name, err)
		}
	}
	// 금액성 대상은 import:balance(재무)만. 관리자는 마스터만.
	if _, err := e.svc.Template(as(e.admin), domain.ImportOpeningStock); !isErr(err, contract.ErrForbidden) {
		t.Errorf("관리자의 기초 재고 양식: %v", err)
	}
	if _, err := e.svc.Template(as(e.finance), domain.ImportOpeningStock); err != nil {
		t.Errorf("재무의 기초 재고 양식: %v", err)
	}
	if _, err := e.svc.Template(as(e.admin), "nope"); !isErr(err, contract.ErrValidation) {
		t.Errorf("알 수 없는 대상: %v", err)
	}
	// 어댑터가 없는 대상은 안내와 함께 거부한다.
	if _, err := e.svc.Preview(as(e.finance), domain.ImportBudget, "", file); !isErr(err, contract.ErrInvalidState) {
		t.Errorf("어댑터 없음: %v", err)
	}
	// 기준일이 필요한 대상은 기준일 형식·오늘 이하를 먼저 본다.
	e.svc.RegisterTarget(&dateHandler{})
	for _, bad := range []string{"", "2026-13-01", "2026-12-01"} {
		if _, err := e.svc.Preview(as(e.finance), domain.ImportOpeningStock, bad, file); !isErr(err, contract.ErrValidation) {
			t.Errorf("기준일 %q: %v", bad, err)
		}
	}
	// 같은 대상의 동시 가져오기는 한쪽만 진행한다.
	unlock, err := e.svc.lock(domain.ImportWarehouse)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file); !isErr(err, contract.ErrInvalidState) || !strings.Contains(err.Error(), "진행 중") {
		t.Errorf("동시 진행: %v", err)
	}
	unlock()
	// 파일 수준 오류(형식·크기)는 한국어 검증 오류다.
	if _, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", []byte("a,b\n1,2\n")); !isErr(err, contract.ErrValidation) {
		t.Errorf("CSV: %v", err)
	}
	// 대상 카드: 권한이 없으면 비활성과 사유, 어댑터가 없으면 연결 안내.
	ts, err := e.svc.Targets(as(e.admin))
	if err != nil || len(ts) != 7 {
		t.Fatalf("대상 카드: %v", err)
	}
	for _, c := range ts {
		switch c.Target {
		case domain.ImportWarehouse:
			if !c.Allowed || !c.Ready || c.Reason != "" || len(c.Columns) == 0 {
				t.Errorf("창고 카드: %+v", c)
			}
		case domain.ImportOpeningStock:
			if c.Allowed || !strings.Contains(c.Reason, "권한") {
				t.Errorf("관리자의 기초 재고 카드: %+v", c)
			}
		}
	}
	if _, err := e.svc.Targets(as(e.staff)); !isErr(err, contract.ErrForbidden) {
		t.Errorf("직원의 대상 카드: %v", err)
	}
}

type dateHandler struct{ fakeHandler }

func (h *dateHandler) Target() string { return domain.ImportOpeningStock }

// 금액이 담긴 이력 요약은 원장 조회·재고 평가 조회 권한이 없으면 가린다(명세 5절).
func TestMoneySummaryMaskedWithoutAmountPermissions(t *testing.T) {
	e := newEnv(t)
	b := domain.ImportBatch{Target: domain.ImportOpeningStock, Summary: []string{"재고 금액 합 1,000원"}}
	if got := e.svc.maskAmounts(e.finance, b); len(got.Summary) != 1 {
		t.Error("재무는 금액을 본다")
	}
	noAmt := contract.Principal{UserID: 9, Perms: contract.NewPermSet(contract.PermImportRead)}
	if got := e.svc.maskAmounts(noAmt, b); got.Summary != nil {
		t.Error("권한이 없으면 가려야 함")
	}
	b.Target = domain.ImportWarehouse
	if got := e.svc.maskAmounts(noAmt, b); len(got.Summary) != 1 {
		t.Error("마스터 대상은 가리지 않는다")
	}
}
