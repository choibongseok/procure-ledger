package service

import (
	"context"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 세금계산서 사업자번호 원문 열람 기록(B41 5.2, 명세 43 D14). 재무 화면에는 사업자번호가 원문(업무 식별자)으로 나가므로 거래처 목록과 같은 기준
// (account 범주, business_no 필드)으로 응답 전에 기록한다. 기록하지 못하면 원문을 싣지 않고 실패한다(fail-closed).
//   - 정보주체는 사업자번호가 같은 등록 거래처(vendor)다. 거래처와 매칭되지 않는 상대방(미등록 공급자·구매자)은 counterparty(subject_id = 세금계산서 번호)로 기록한다.
//   - 우리 회사 자신의 사업자번호는 정보주체가 아니므로 기록하지 않는다. MCP·API 채널은 가려서 나가므로 기록할 원문이 없다.
//   - 이메일은 1단계에서 항상 가려서 나가므로 기록 대상이 아니다.

// present 는 응답 한 건을 가리고 원문 사업자번호를 기록한다.
func (s *TaxInvoiceService) present(ctx context.Context, p contract.Principal, v domain.TaxInvoice) (domain.TaxInvoice, error) {
	out := s.out(p, v)
	return out, s.noteTaxRaw(ctx, p, privacy.ScreenTaxInvoiceDetail, []domain.TaxInvoice{out})
}

// presentList 는 목록을 가리고 응답 한 번에 한 번 기록한다.
func (s *TaxInvoiceService) presentList(ctx context.Context, p contract.Principal, rows []domain.TaxInvoice) ([]domain.TaxInvoice, error) {
	for i := range rows {
		rows[i] = s.out(p, rows[i])
	}
	return rows, s.noteTaxRaw(ctx, p, privacy.ScreenTaxInvoiceList, rows)
}

func (s *TaxInvoiceService) noteTaxRaw(ctx context.Context, p contract.Principal, screen string, rows []domain.TaxInvoice) error {
	if s.Privacy == nil || p.Channel == contract.ChannelMCP || p.Channel == contract.ChannelAPI || len(rows) == 0 {
		return nil
	}
	company, err := s.companyParty(ctx, s.DB)
	if err != nil {
		return err
	}
	vendors := map[string]int64{} // 사업자번호(숫자) → 가장 작은 거래처 번호
	rs, err := s.DB.QueryContext(ctx, `SELECT id, business_no FROM vendors WHERE business_no <> '' ORDER BY id`)
	if err != nil {
		return err
	}
	for rs.Next() {
		var id int64
		var raw string
		if err := rs.Scan(&id, &raw); err != nil {
			rs.Close()
			return err
		}
		if _, ok := vendors[digitsOnly(raw)]; !ok {
			vendors[digitsOnly(raw)] = id
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	vendorIDs := map[int64]bool{}
	var subs []privacy.Subject
	for _, v := range rows {
		counter := false
		for _, b := range []string{v.Supplier.BizNo, v.Buyer.BizNo} {
			if len(b) != 10 || digitsOnly(b) != b || b == company.BizNo { // 가려진 값(별표)과 우리 회사 번호는 대상이 아니다
				continue
			}
			if id, ok := vendors[b]; ok {
				if !vendorIDs[id] {
					vendorIDs[id] = true
					subs = append(subs, privacy.Subject{Type: privacy.SubjectVendor, ID: id})
				}
			} else {
				counter = true
			}
		}
		if counter { // 거래처로 등록되지 않은 상대방: 정보주체는 이 세금계산서(subject_id = tax_invoices.id)
			subs = append(subs, privacy.Subject{Type: privacy.SubjectCounterparty, ID: v.ID})
		}
	}
	if len(subs) == 0 {
		return nil
	}
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].Type != subs[j].Type {
			return subs[i].Type < subs[j].Type
		}
		return subs[i].ID < subs[j].ID
	})
	return s.notePrivacy(ctx, p, privacy.Access{Category: privacy.CatAccount, Screen: screen, Fields: []string{privacy.FieldBusinessNo}, Subjects: subs})
}
