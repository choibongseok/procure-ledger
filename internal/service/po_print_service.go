package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 발주서 출력(B25 ③, 3.3). 읽기 전용 조립이고, 부가세는 여기서만 계산한다(발주일 기준 부가세율, 원 미만 버림).
// 구매요청을 볼 수 있는 사람만 본다(범위 밖은 404). 사업자번호·연락처는 mask:bypass 가 없으면 가려서 내보낸다(fail-closed).

const poPrintNote = "납품 시 거래명세서를 지참해 주세요. 부분 납품은 입고 처리 뒤 남은 수량으로 안내합니다."

// POPrintService 는 domain.POPrintService 의 구현이다.
type POPrintService struct {
	Base
	Purchases repository.SQLPurchases
	Inventory repository.SQLInventory
	Rating    repository.SQLRating
}

var _ domain.POPrintService = (*POPrintService)(nil)

func NewPOPrintService(b Base) *POPrintService { return &POPrintService{Base: b} }

// load 는 열람 범위 안의 구매요청과 그 발주를 읽는다. 승인 전이거나 발주가 없으면 한국어 안내와 함께 거부한다.
func (s *POPrintService) load(ctx context.Context, prID int64) (contract.Principal, repository.PRRecord, domain.PurchaseOrder, error) {
	p, err := contract.Authorize(ctx, contract.PermPurchaseRead)
	if err != nil {
		return p, repository.PRRecord{}, domain.PurchaseOrder{}, err
	}
	scope, err := s.viewScope(ctx, p)
	if err != nil {
		return p, repository.PRRecord{}, domain.PurchaseOrder{}, err
	}
	rec, err := s.Purchases.GetPR(ctx, s.DB, prID, scope)
	if err != nil {
		return p, rec, domain.PurchaseOrder{}, err
	}
	if !isPurchaseKind(rec.FormKind) {
		return p, rec, domain.PurchaseOrder{}, contract.ErrNotFound
	}
	if rec.Status != domain.PRApproved && rec.Status != domain.PROrdered {
		return p, rec, domain.PurchaseOrder{}, fmt.Errorf("%w: 아직 승인되지 않은 구매요청이라 발주서가 없습니다. 결재가 끝나면 발주서를 출력할 수 있습니다.", contract.ErrInvalidState)
	}
	if rec.PurchaseOrderID == 0 {
		return p, rec, domain.PurchaseOrder{}, fmt.Errorf("%w: 이 구매요청으로 만들어진 발주가 없습니다.", contract.ErrNotFound)
	}
	po, err := s.Inventory.GetPO(ctx, s.DB, rec.PurchaseOrderID, scope)
	return p, rec, po, err
}

// Print 는 발주서 내용을 만든다.
func (s *POPrintService) Print(ctx context.Context, prID int64) (domain.POPrint, error) {
	p, rec, po, err := s.load(ctx, prID)
	if err != nil {
		return domain.POPrint{}, err
	}
	out := domain.POPrint{PRID: rec.ID, PRNo: rec.No, PONo: po.No, PODate: po.CreatedAt, Requester: rec.RequesterName, Department: rec.RequesterUnitPath,
		DeliveryPlace: po.DeliveryPlace, DueDate: po.DueDate, Note: poPrintNote, Masked: !contract.RawFieldAllowed(p)}
	if out.DeliveryPlace == "" {
		out.DeliveryPlace = defaultDeliveryPlace
	}
	vendors, err := s.Inventory.ListVendors(ctx, s.DB)
	if err != nil {
		return domain.POPrint{}, err
	}
	for _, v := range vendors {
		if v.ID != po.VendorID {
			continue
		}
		out.SupplierName = v.Name
		out.ContactEmpty = v.Phone == "" && v.Email == ""
		m := s.maskVendor(p, v)
		out.BusinessNo, out.Phone, out.Email = m.BusinessNo, m.Phone, m.Email
		// 발주서에 사업자번호·연락처 원문이 실리면 기록한다(계좌번호는 발주서에 싣지 않는다). 기록하지 못하면 발주서를 내주지 않는다.
		if rf := vendorRawFields(v, m); len(rf) > 0 {
			var shown []string
			for _, f := range rf {
				if f != privacy.FieldAccountNo {
					shown = append(shown, f)
				}
			}
			if len(shown) > 0 {
				if err := s.noteAll(ctx, p, vendorAccesses(privacy.ScreenPOPrint, map[int64][]string{v.ID: shown})); err != nil {
					return domain.POPrint{}, err
				}
			}
		}
	}
	// 발주처 회사 이름은 조직도 최상위 조직이다(조직 정보가 없으면 비워 둔다).
	if s.Org != nil {
		if person, err := s.Org.Person(ctx, rec.RequesterID, contract.DayOf(s.now())); err == nil && len(person.Primary.Path) > 0 {
			out.Company = person.Primary.Path[0].Name
			if out.Department == "" {
				out.Department = contract.OrgPathText(person.Primary.Path)
			}
		} else if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
			return domain.POPrint{}, err
		}
	}
	// 회사 설정이 있으면 그 값이 우선이다(회사명·사업자번호·대표자·주소·전화·로고·직인). 없으면 위의 조직도 이름만 쓴다(현행과 같다).
	if ci, err := s.companyInfo(ctx, s.DB); err != nil {
		return domain.POPrint{}, err
	} else if ci.Configured {
		out.Company, out.CompanyBizNo, out.CompanyRep, out.CompanyAddr, out.CompanyPhone = ci.Name, ci.BusinessNo, ci.Representative, ci.Address, ci.Phone
		out.HasLogo, out.HasSeal = ci.HasLogo, ci.HasSeal
	}
	ids := make([]int64, len(po.Lines))
	for i, l := range po.Lines {
		ids[i] = l.ItemID
	}
	units, err := s.Rating.ItemUnits(ctx, s.DB, ids)
	if err != nil {
		return domain.POPrint{}, err
	}
	for i, l := range po.Lines {
		amount := l.Quantity * l.UnitPrice
		out.Lines = append(out.Lines, domain.POPrintLine{No: i + 1, ItemName: l.ItemName, Unit: units[l.ItemID], Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: amount})
		out.Supply += amount
	}
	// 부가세율은 발주일(거래일)에 유효한 세율이다(효력일 이력). 세율이 바뀐 뒤에 열어도 과거 발주서의 부가세는 같다.
	rate, err := s.vatRateOn(ctx, s.DB, string(contract.DayOf(po.CreatedAt)))
	if err != nil {
		return domain.POPrint{}, err
	}
	out.VATRate = rate
	out.VAT = domain.VATFromSupply(out.Supply, rate)
	out.Total = out.Supply + out.VAT
	return out, nil
}

// RecordPrint 는 인쇄 버튼을 눌렀을 때 감사(po.print)를 남긴다. 화면을 여는 것만으로는 남기지 않는다.
func (s *POPrintService) RecordPrint(ctx context.Context, prID int64) error {
	p, rec, po, err := s.load(ctx, prID)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "po.print", audit.TargetPurchaseOrder, po.ID, func(tx contract.Tx) error {
		return s.record(ctx, tx, p, "po.print", audit.TargetPurchaseOrder, po.ID,
			audit.DiffItem{Key: "po_no", New: po.No}, audit.DiffItem{Key: "pr_no", New: rec.No})
	})
}

// maskVendor 는 사업자번호·계좌·연락처를 mask:bypass 가 없으면 가린다. Masker 가 없으면 전부 가린다(fail-closed).
func (b Base) maskVendor(p contract.Principal, v domain.Vendor) domain.Vendor {
	m := func(kind contract.FieldKind, val string) string {
		if val == "" {
			return ""
		}
		if b.Masker == nil {
			return "****"
		}
		return b.Masker.MaskField(p, kind, val)
	}
	v.BusinessNo = m(contract.FieldBusinessNo, v.BusinessNo)
	v.AccountNo = m(contract.FieldAccountNo, v.AccountNo)
	v.Phone = m(contract.FieldPhone, v.Phone)
	v.Email = m(contract.FieldEmail, v.Email)
	return v
}
