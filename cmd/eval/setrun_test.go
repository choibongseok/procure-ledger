package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/evalset"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/migrations"
)

// seedSet 은 시험 코드가 직접 만든 세트다(시드 없음). mock 공급자는 eval.run 용도에 질문을 그대로 돌려준다.
func seedSet(t *testing.T, path string) {
	t.Helper()
	db, err := dbutil.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	svc, err := evalset.New(evalset.Options{DB: db, Txs: contract.SQLTxManager{DB: db}, Audit: audit.New(db), Masker: rbac.Masker{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO departments (id, name) VALUES (900, '운영')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, department_id) VALUES (1, 'admin@pl.test', '관리자', 'admin', 900)`); err != nil {
		t.Fatal(err)
	}
	ctx := contract.WithPrincipal(context.Background(), contract.Principal{UserID: 1, Role: contract.RoleAdmin, Channel: contract.ChannelWeb, Auth: contract.AuthSession, Perms: contract.NewPermSet(contract.PermEvalRead, contract.PermEvalWrite)})
	if _, err := svc.CreateSet(ctx, evalset.SetInput{Code: "cli-set", Name: "명령줄 시험", Purpose: evalset.PurposeText}); err != nil {
		t.Fatal(err)
	}
	for _, it := range []evalset.ItemInput{
		{Question: "연차는 15일입니다", Expected: "15일", Grader: evalset.GraderContains},
		{Question: "출장비 한도는 얼마인가요", Expected: "응답에 없는 글", Grader: evalset.GraderContains},
	} {
		if _, err := svc.SaveItem(ctx, "cli-set", it); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Publish(ctx, "cli-set", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetModeRunsMockAndStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "set.db")
	seedSet(t, path)
	var out, errw bytes.Buffer
	code := run([]string{"--set", "cli-set", "--provider", "mock", "--db", path}, &out, &errw)
	if code != 1 || !strings.Contains(out.String(), "합격률 50% (1/2)") || !strings.Contains(out.String(), "실패 문항 2") || !strings.Contains(out.String(), "기준 미달") {
		t.Fatalf("code=%d\nout=%s\nerr=%s", code, out.String(), errw.String())
	}
	if strings.Contains(out.String(), "응답에 없는 글") {
		t.Fatal("기대 답 값이 출력에 있으면 안 된다")
	}
	db, _ := dbutil.Open(path)
	defer db.Close()
	var n, items int
	var mode, status string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(mode), MAX(status) FROM eval_runs WHERE set_id > 0`).Scan(&n, &mode, &status); err != nil || n != 1 || mode != "cli" || status != "done" {
		t.Fatalf("저장: %d %s %s %v", n, mode, status, err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM eval_run_items`).Scan(&items)
	var aud int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'eval.run'`).Scan(&aud)
	if items != 2 || aud != 1 {
		t.Fatalf("문항 %d, 감사 %d", items, aud)
	}
	// 버전 지정·잘못된 입력.
	var o2, e2 bytes.Buffer
	if c := run([]string{"--set", "cli-set@1", "--provider", "mock,mock", "--db", path}, &o2, &e2); c != 1 || !strings.Contains(o2.String(), "공급자별 세트 비교") {
		t.Fatalf("버전 지정·비교: %d %s %s", c, o2.String(), e2.String())
	}
	if c := run([]string{"--set", "cli-set", "--provider", "mock"}, &o2, &e2); c != 2 {
		t.Fatalf("--db 없이: %d", c)
	}
	if c := run([]string{"--set", "cli-set@x", "--provider", "mock", "--db", path}, &o2, &e2); c != 2 {
		t.Fatalf("잘못된 버전: %d", c)
	}
	if c := run([]string{"--set", "nope", "--provider", "mock", "--db", path}, &o2, &e2); c != 2 {
		t.Fatalf("없는 세트: %d", c)
	}
}
