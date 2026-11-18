package service_test

import (
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/importflow"
	"github.com/choibongseok/procure-ledger/internal/importx"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 공통 가져오기 흐름(platform)·파서(ai)·대상 어댑터(erp)를 실제로 이어 본다: 양식 파일 그대로 미리보기 → 반영 → 멱등.
// (양식의 예시 한 줄이 데이터 행으로 읽히므로 그 한 줄이 추가로 판정된다.)
func TestImportFlowWithRealParserAndAdapters(t *testing.T) {
	e := newEnv(t)
	flow := importflow.New(importflow.Options{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Parser: importx.Parser{}, Templates: importx.Templates{}, Now: func() time.Time { return e.now }})
	b := e.importBase()
	for _, h := range []domain.ImportTargetHandler{service.NewImportAccount(b), service.NewImportWarehouse(b), service.NewImportItem(b)} {
		flow.RegisterTarget(h)
	}
	ctx := as(uFinance)
	for _, target := range []string{domain.ImportAccount, domain.ImportWarehouse, domain.ImportItem} {
		tpl, err := flow.Template(ctx, target)
		if err != nil {
			t.Fatalf("%s 양식: %v", target, err)
		}
		pv, err := flow.Preview(ctx, target, "", tpl.Data)
		if err != nil || !pv.Applicable || pv.Counts.Add != 1 || pv.Counts.Err != 0 || pv.Token == "" {
			t.Fatalf("%s 미리보기: %+v %v", target, pv, err)
		}
		batch, err := flow.Apply(ctx, target, pv.BatchNo, pv.Token, tpl.Data)
		if err != nil || batch.Status != domain.ImportApplied || batch.Counts.Add != 1 {
			t.Fatalf("%s 반영: %+v %v", target, batch, err)
		}
		again, err := flow.Preview(ctx, target, "", tpl.Data)
		if err != nil || again.Applicable || again.Counts.Same != 1 || again.Counts.Add != 0 {
			t.Fatalf("%s 같은 파일은 동일: %+v %v", target, again, err)
		}
	}
	if e.col(t, `SELECT COUNT(*) FROM accounts WHERE code = '5310'`) != "1" {
		t.Error("양식 예시의 계정과목이 반영되지 않음")
	}
	// 마스터 권한이 없는 역할은 거부된다(서비스도 권한을 다시 확인한다).
	tpl, _ := flow.Template(ctx, domain.ImportItem)
	if _, err := flow.Preview(as(uStaff), domain.ImportItem, "", tpl.Data); !isErr(err, contract.ErrForbidden) {
		t.Errorf("직원 미리보기: %v", err)
	}
}
