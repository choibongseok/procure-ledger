package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// B31 자동 매입 전표 골든셋(evals/journals.json)의 채점기. 실제 파이프라인(증빙 업로드 → 판정(mock 추출) → 재무 확정 → 전표 훅)을 돌리고
// 만들어진 전표와 전표 대기를 기대값과 비교한다. 전표를 만드는 쪽(erp 의 전표 서비스)은 journalRig 로 꽂는다.

// journalResult 는 한 케이스가 끝난 뒤 장부에 남은 것이다.
type journalResult struct {
	Journals     []evals.JournalExpect1 // 취소되지 않은 자동 매입 전표(줄은 차변 먼저, 계정 코드 순이 아니라 생성 순서 그대로)
	QueueReasons []string               // 전표 대기 사유 코드(mismatch_confirmed·hold_confirmed·rule_account_inactive·total_differs)
}

// journalRig 는 전표 서비스 어댑터다. 기본 실행에는 연결되어 있지 않고(newJournalRig == nil) 연결되면 journals 묶음이 합격 기준에 들어간다.
type journalRig interface {
	// Hook 은 docverify 확정에 꽂을 전표 훅이다.
	Hook() domain.PurchaseJournalHook
	// SetInactiveAccounts 는 케이스가 중지시키는 계정 코드 목록을 적용한다(이전 케이스의 상태를 되돌린 뒤).
	SetInactiveAccounts(ctx context.Context, codes []string) error
	// Result 는 이 발주(poID)에 대해 만들어진 전표와 대기 행을 돌려준다.
	Result(ctx context.Context, poID int64) (journalResult, error)
}

// newJournalRig 는 전표 서비스(B31 erp 2단계)가 구현되면 그 어댑터를 만드는 함수로 채운다. nil 이면 journals 묶음은 건너뛴다.
var newJournalRig func(db *sql.DB, txs contract.TxManager, rec audit.Recorder) (journalRig, error)

func confirmOutcome(s string) domain.Outcome {
	switch s {
	case "match":
		return domain.OutcomeMatch
	case "mismatch":
		return domain.OutcomeMismatch
	}
	return domain.OutcomeHold
}

func docKind(s string) string {
	if s == "tax_invoice" {
		return "세금계산서"
	}
	return "거래명세서"
}

// runJournals 는 골든셋 케이스마다 발주·입고를 시드하고 증빙을 올려 판정·확정한 뒤 전표 결과를 채점한다.
func runJournals(ctx context.Context, db *sql.DB, txs contract.TxManager, rec audit.Recorder, gw *llmgw.Gateway, set evals.JournalSet, rig journalRig) ([]CaseResult, error) {
	inv := kit.NewInventory()
	svc := &docverify.Service{DB: db, Txs: txs, Audit: rec, LLM: gw, Inventory: inv, Vendors: docverify.SQLVendors{DB: db}, Masker: rbac.Masker{}, JournalHook: rig.Hook()}
	var out []CaseResult
	for _, c := range set.Cases {
		cr, err := runJournalCase(ctx, db, svc, inv, rig, c)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, nil
}

func runJournalCase(ctx context.Context, db *sql.DB, svc *docverify.Service, inv *kit.Inventory, rig journalRig, c evals.JournalCase) (CaseResult, error) {
	cr := CaseResult{Suite: "journals", ID: c.ID, Critical: c.Critical}
	uploader := contract.WithPrincipal(ctx, kit.Principal(contract.RoleStaff))
	finance := contract.WithPrincipal(ctx, kit.Principal(contract.RoleFinance))
	poID, rcID, err := inv.Seed(ctx, db, c.PO)
	if err != nil {
		return cr, fmt.Errorf("케이스 %s 시드: %w", c.ID, err)
	}
	if err := rig.SetInactiveAccounts(ctx, c.Rules.InactiveAccounts); err != nil {
		return cr, fmt.Errorf("케이스 %s 규칙 적용: %w", c.ID, err)
	}
	for i, d := range c.Docs {
		v, err := svc.Verify(uploader, domain.VerifyInput{Filename: fmt.Sprintf("%s-%d.txt", c.ID, i), ContentType: "text/plain", Text: d.Text, POID: poID, ReceiptID: rcID})
		if err != nil {
			cr.Detail = fmt.Sprintf("증빙 판정 오류: %v", err)
			return cr, nil
		}
		if d.Confirm == "none" {
			continue
		}
		if _, err := svc.Confirm(finance, v.ID, domain.ConfirmInput{Outcome: confirmOutcome(d.Confirm), Comment: "골든셋 확정(" + docKind(d.Kind) + ")"}); err != nil && c.Expect.ConfirmSucceeds {
			cr.Detail = fmt.Sprintf("확정이 성공해야 하는데 실패: %v", err)
			return cr, nil
		}
	}
	res, err := rig.Result(ctx, poID)
	if err != nil {
		return cr, fmt.Errorf("케이스 %s 결과 조회: %w", c.ID, err)
	}
	problems := compareJournals(c.Expect, res)
	cr.Pass = len(problems) == 0
	cr.Detail = fmt.Sprintf("전표 %d건, 대기 %d건", len(res.Journals), len(res.QueueReasons))
	if !cr.Pass {
		cr.Detail = strings.Join(problems, "; ")
	}
	return cr, nil
}

func lineKey(l evals.JournalLine) string { return fmt.Sprintf("%s/%s/%d", l.Account, l.Side, l.Amount) }

func compareJournals(e evals.JournalExpect, got journalResult) []string {
	var p []string
	if e.NoJournal && len(got.Journals) > 0 {
		p = append(p, fmt.Sprintf("전표가 없어야 하는데 %d건", len(got.Journals)))
	}
	if len(e.Journals) != len(got.Journals) && !e.NoJournal {
		p = append(p, fmt.Sprintf("전표 %d건(기대 %d건)", len(got.Journals), len(e.Journals)))
	}
	for i, want := range e.Journals {
		if i >= len(got.Journals) {
			break
		}
		g := got.Journals[i]
		if g.Kind != want.Kind || g.Status != want.Status {
			p = append(p, fmt.Sprintf("전표 %d: 종류·상태 %s/%s (기대 %s/%s)", i+1, g.Kind, g.Status, want.Kind, want.Status))
		}
		if g.Vendor != want.Vendor {
			p = append(p, fmt.Sprintf("전표 %d: 거래처 %q (기대 %q)", i+1, g.Vendor, want.Vendor))
		}
		if g.Evidence != want.Evidence {
			p = append(p, fmt.Sprintf("전표 %d: 연결 증빙 %d건(기대 %d건)", i+1, g.Evidence, want.Evidence))
		}
		var gk, wk []string
		for _, l := range g.Lines {
			gk = append(gk, lineKey(l))
		}
		for _, l := range want.Lines {
			wk = append(wk, lineKey(l))
		}
		sort.Strings(gk)
		sort.Strings(wk)
		if strings.Join(gk, ",") != strings.Join(wk, ",") {
			p = append(p, fmt.Sprintf("전표 %d: 줄 %v (기대 %v)", i+1, gk, wk))
		}
	}
	if e.Queue != nil {
		var gq, wq []string
		gq = append(gq, got.QueueReasons...)
		for _, q := range e.Queue {
			wq = append(wq, q.Reason)
		}
		sort.Strings(gq)
		sort.Strings(wq)
		if strings.Join(gq, ",") != strings.Join(wq, ",") {
			p = append(p, fmt.Sprintf("전표 대기 %v (기대 %v)", gq, wq))
		}
	}
	return p
}
