package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 실제 전표 서비스(B31 erp)를 골든셋에 연결하는 어댑터다. 훅은 service.JournalService 그대로이고, 채점은 DB 에 만들어진 전표·대기 행을 읽어 한다.
func init() {
	newJournalRig = func(db *sql.DB, txs contract.TxManager, rec audit.Recorder) (journalRig, error) {
		b := service.Base{DB: db, Txs: txs, Audit: rec}
		return &serviceRig{db: db, journals: service.NewJournalService(b, nil), accounts: service.NewAccountService(b)}, nil
	}
}

type serviceRig struct {
	db       *sql.DB
	journals *service.JournalService
	accounts *service.AccountService
}

func (r *serviceRig) Hook() domain.PurchaseJournalHook { return r.journals }

// 케이스가 중지시키는 계정 코드 → 그 계정을 쓰는 규칙 키. 시스템 계정은 중지할 수 없으므로 규칙이 새 계정을 가리키게 하고 그 계정을 중지한다.
var ruleKeyOfCode = map[string]string{"1210": "purchase.inventory", "1310": "purchase.vat", "2110": "purchase.payable"}

func (r *serviceRig) SetInactiveAccounts(ctx context.Context, codes []string) error {
	fin := contract.WithPrincipal(ctx, kit.Principal(contract.RoleFinance))
	// 이전 케이스의 상태를 되돌린다: 규칙을 원래 계정으로.
	for code, key := range ruleKeyOfCode {
		if err := r.accounts.SetRule(fin, key, code); err != nil {
			return err
		}
	}
	for i, code := range codes {
		key, ok := ruleKeyOfCode[code]
		if !ok {
			return fmt.Errorf("중지할 수 없는 규칙 계정 %s", code)
		}
		alt := fmt.Sprintf("%d", 1290+i)
		if code[0] == '2' {
			alt = fmt.Sprintf("%d", 2190+i)
		}
		if _, err := r.accounts.Create(fin, domain.AccountInput{Code: alt, Name: "평가용 " + alt}); err != nil && !isDup(err) {
			return err
		}
		if err := r.accounts.SetRule(fin, key, alt); err != nil {
			return err
		}
		if _, err := r.accounts.SetActive(fin, alt, false); err != nil {
			return err
		}
	}
	return nil
}

func isDup(err error) bool { return err != nil && errors.Is(err, contract.ErrValidation) }

func (r *serviceRig) Result(ctx context.Context, poID int64) (journalResult, error) {
	var res journalResult
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT j.id, j.kind, j.status FROM journals j JOIN journal_evidence e ON e.journal_id = j.id AND e.kind = 'verdict'
		JOIN document_verdicts v ON v.id = e.ref_id WHERE v.po_id = ? AND j.kind = 'auto_purchase' AND j.status <> 'canceled' ORDER BY j.id`, poID)
	if err != nil {
		return res, err
	}
	type head struct {
		id           int64
		kind, status string
	}
	var heads []head
	for rows.Next() {
		var h head
		if err := rows.Scan(&h.id, &h.kind, &h.status); err != nil {
			rows.Close()
			return res, err
		}
		heads = append(heads, h)
	}
	rows.Close()
	for _, h := range heads {
		j := evals.JournalExpect1{Kind: h.kind, Status: h.status}
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_evidence WHERE journal_id = ?`, h.id).Scan(&j.Evidence); err != nil {
			return res, err
		}
		lr, err := r.db.QueryContext(ctx, `SELECT a.code, l.side, l.amount, COALESCE(v.name,'') FROM journal_lines l JOIN accounts a ON a.id = l.account_id LEFT JOIN vendors v ON v.id = l.vendor_id WHERE l.journal_id = ? ORDER BY l.seq`, h.id)
		if err != nil {
			return res, err
		}
		for lr.Next() {
			var l evals.JournalLine
			var vendor string
			if err := lr.Scan(&l.Account, &l.Side, &l.Amount, &vendor); err != nil {
				lr.Close()
				return res, err
			}
			if vendor != "" {
				j.Vendor = vendor
			}
			j.Lines = append(j.Lines, l)
		}
		lr.Close()
		res.Journals = append(res.Journals, j)
	}
	qr, err := r.db.QueryContext(ctx, `SELECT reason FROM journal_queue WHERE source_type = 'verdict' AND po_id = ? ORDER BY id`, poID)
	if err != nil {
		return res, err
	}
	defer qr.Close()
	for qr.Next() {
		var s string
		if err := qr.Scan(&s); err != nil {
			return res, err
		}
		res.QueueReasons = append(res.QueueReasons, s)
	}
	return res, qr.Err()
}
