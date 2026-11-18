package service

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 상대방 이메일 원문 열람(B43 2단계, 명세 43 14.3·D14). 이메일은 `contact` 범주라 응답에서는 항상 가려서 나가고, 원문은 이 경로로만 나간다.
//   - 권한: taxinvoice:read + 원문 허용(mask:bypass, MCP·API 채널 제외). 둘 다 있어야 하고 기록이 남지 않으면 싣지 않는다(fail-closed).
//   - 정보주체: 사업자번호가 같은 등록 거래처(vendor), 거래처가 아니면 counterparty(subject_id = 이 세금계산서 번호)다.
//   - 우리 회사 쪽 이메일은 대상이 아니다. 감사 diff 에는 이메일을 넣지 않는다(열람 기록이 privacy.Note 로 남는다).

var _ domain.TaxContactService = (*TaxInvoiceService)(nil)

// RevealContact 는 세금계산서 상대방(매출이면 공급받는자, 매입이면 공급자)의 이메일 원문이다.
func (s *TaxInvoiceService) RevealContact(ctx context.Context, id int64) (domain.TaxContact, error) {
	p, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead)
	if err != nil {
		return domain.TaxContact{}, err
	}
	if !contract.RawFieldAllowed(p) {
		return domain.TaxContact{}, fmt.Errorf("%w: 상대방 이메일 원문을 볼 권한이 없습니다", contract.ErrForbidden)
	}
	v, err := s.Repo.Get(ctx, s.DB, id)
	if err != nil {
		return domain.TaxContact{}, err
	}
	other := v.Buyer
	if v.Direction == domain.TaxPurchase {
		other = v.Supplier
	}
	out := domain.TaxContact{Name: other.Name}
	if other.Email == "" {
		return out, nil // 보여 줄 값이 없으면 기록할 열람도 없다
	}
	vid, err := s.vendorByBiz(ctx, other.BizNo)
	if err != nil {
		return domain.TaxContact{}, err
	}
	subject := privacy.Subject{Type: privacy.SubjectVendor, ID: vid}
	if vid == 0 { // 거래처로 등록되지 않은 상대방: 정보주체는 이 세금계산서(subject_id = tax_invoices.id)
		subject = privacy.Subject{Type: privacy.SubjectCounterparty, ID: v.ID}
	}
	if err := s.notePrivacy(ctx, p, privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenTaxInvoiceDetail, Fields: []string{privacy.FieldEmail},
		Subjects: []privacy.Subject{subject}}); err != nil {
		return domain.TaxContact{}, err
	}
	out.Email = other.Email
	return out, nil
}

// vendorByBiz 는 사업자번호(숫자 10자리)가 같은 등록 거래처 중 번호가 가장 작은 것이다(없으면 0).
func (s *TaxInvoiceService) vendorByBiz(ctx context.Context, biz string) (int64, error) {
	if len(biz) != 10 {
		return 0, nil
	}
	rs, err := s.DB.QueryContext(ctx, `SELECT id, business_no FROM vendors WHERE business_no <> '' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rs.Close()
	for rs.Next() {
		var id int64
		var raw string
		if err := rs.Scan(&id, &raw); err != nil {
			return 0, err
		}
		if digitsOnly(raw) == biz {
			return id, nil
		}
	}
	return 0, rs.Err()
}
