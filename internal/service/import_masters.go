package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 기초 데이터 가져오기(B35)의 마스터 대상 어댑터: 계정과목·창고·거래처·품목( 3.3).
// 파서(internal/importx)가 형식·길이·필수·열거를 이미 검증했다. 어댑터는 키로 DB 와 비교하고(Plan, 읽기 전용), 같은 트랜잭션에서 저장한다(Apply).
// 저장 규칙은 화면 입력과 같다: 계정 구분·코드는 바꿀 수 없고 시스템 계정은 중지할 수 없으며, 거래처 계좌번호는 봉해서 저장한다.
// 판정·감사·이력에는 원본 민감 값(계좌번호·연락처)을 내지 않는다(끝 4자리만).

// importSource 는 감사 detail 에 덧붙이는 출처 문구다.
func importSource(in domain.ImportPlanInput) string {
	if in.BatchNo == "" {
		return ""
	}
	return " (기초 데이터 가져오기 " + in.BatchNo + ")"
}

func maskTail(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

func maskEmail(s string) string {
	if i := strings.LastIndex(s, "@"); i > 0 {
		return "***" + s[i:]
	}
	return "****"
}

func verdictOf(row domain.ImportRow, key, verdict, reason string, changes []domain.ImportChange) domain.ImportRowVerdict {
	return domain.ImportRowVerdict{RowNo: row.RowNo, Key: key, Verdict: verdict, Reason: reason, Changes: changes}
}

func chg(col, old, now string) domain.ImportChange {
	return domain.ImportChange{Column: col, Old: old, New: now}
}

func decide(row domain.ImportRow, key string, changes []domain.ImportChange) domain.ImportRowVerdict {
	if len(changes) == 0 {
		return verdictOf(row, key, domain.ImportVerdictSame, "", nil)
	}
	return verdictOf(row, key, domain.ImportVerdictChange, "", changes)
}

// ---- ① 계정과목 ----

// ImportAccountHandler 는 계정과목 어댑터다.
type ImportAccountHandler struct {
	Base
	Repo repository.SQLLedger
}

var _ domain.ImportTargetHandler = (*ImportAccountHandler)(nil)

func NewImportAccount(b Base) *ImportAccountHandler { return &ImportAccountHandler{Base: b} }

func (h *ImportAccountHandler) Target() string { return domain.ImportAccount }

var accountActiveLabel = map[bool]string{true: "사용", false: "중지"}

func (h *ImportAccountHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	for _, r := range rows {
		code, name, kind := r.Cells["code"], r.Cells["name"], r.Cells["kind"]
		active := r.Cells["active"] != "inactive"
		if kind != kindOfCode(code) {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictError, "구분이 코드의 앞 숫자가 뜻하는 구분과 다릅니다.", nil))
			continue
		}
		cur, found, err := h.Repo.AccountByCode(ctx, q, code)
		if err != nil {
			return plan, err
		}
		if !found {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictAdd, "", nil))
			continue
		}
		if cur.Kind != kind {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictError, "계정 구분은 바꿀 수 없습니다. 코드와 구분은 그대로 두고 이름만 바꿀 수 있습니다.", nil))
			continue
		}
		if !active && cur.System {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictError, "시스템 계정은 중지할 수 없습니다. 자동 전표 규칙이 쓰는 계정입니다.", nil))
			continue
		}
		var cs []domain.ImportChange
		if cur.Name != name {
			cs = append(cs, chg("이름", cur.Name, name))
		}
		if cur.Active != active {
			cs = append(cs, chg("사용 여부", accountActiveLabel[cur.Active], accountActiveLabel[active]))
		}
		plan.Verdicts = append(plan.Verdicts, decide(r, code, cs))
	}
	return plan, nil
}

func (h *ImportAccountHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	for i, r := range rows {
		v := plan[i]
		if v.Verdict != domain.ImportVerdictAdd && v.Verdict != domain.ImportVerdictChange {
			continue
		}
		code, name := r.Cells["code"], r.Cells["name"]
		active := r.Cells["active"] != "inactive"
		if v.Verdict == domain.ImportVerdictAdd {
			if err := h.Repo.InsertAccount(ctx, tx, code, name, kindOfCode(code), h.now()); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if !active {
				if err := h.Repo.SetAccountActive(ctx, tx, code, false); err != nil {
					return domain.ImportApplyOutcome{}, err
				}
			}
			if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.create", TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess,
				Detail: "계정과목 추가" + importSource(in), Diff: []audit.DiffItem{{Key: "code", New: code}, {Key: "name", New: name}}}); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			continue
		}
		cur, _, err := h.Repo.AccountByCode(ctx, tx, code)
		if err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		if cur.Name != name {
			if err := h.Repo.RenameAccount(ctx, tx, code, name); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "account.update", TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess,
				Detail: "계정과목 이름 변경" + importSource(in), Diff: []audit.DiffItem{{Key: "name", Old: cur.Name, New: name}}}); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
		}
		if cur.Active != active {
			action := "account.deactivate"
			if active {
				action = "account.activate"
			}
			if err := h.Repo.SetAccountActive(ctx, tx, code, active); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetAccount, TargetID: code, Result: audit.ResultSuccess,
				Detail: "계정과목 사용 여부 변경" + importSource(in), Diff: []audit.DiffItem{{Key: "active", Old: fmt.Sprint(cur.Active), New: fmt.Sprint(active)}}}); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
		}
	}
	return domain.ImportApplyOutcome{}, nil
}

// ---- ② 창고 ----

// ImportWarehouseHandler 는 창고 어댑터다.
type ImportWarehouseHandler struct {
	Base
	Stock repository.SQLStock
}

var _ domain.ImportTargetHandler = (*ImportWarehouseHandler)(nil)

func NewImportWarehouse(b Base) *ImportWarehouseHandler { return &ImportWarehouseHandler{Base: b} }

func (h *ImportWarehouseHandler) Target() string { return domain.ImportWarehouse }

func (h *ImportWarehouseHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	ws, err := h.Stock.Warehouses(ctx, q)
	if err != nil {
		return plan, err
	}
	seenCode := map[string]int{} // 파일 안 같은 창고 코드(대소문자 무시)
	for _, r := range rows {
		code, name, loc := strings.ToUpper(r.Cells["code"]), r.Cells["name"], r.Cells["location"]
		if first, dup := seenCode[code]; dup {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 창고 코드가 두 번 있습니다(%d번째 줄과 같은 코드).", first), nil))
			continue
		}
		seenCode[code] = r.RowNo
		var cur *domain.Warehouse
		for i := range ws {
			if strings.EqualFold(ws[i].Code, code) {
				cur = &ws[i]
			}
		}
		clash := false
		for _, w := range ws {
			if (cur == nil || w.ID != cur.ID) && strings.EqualFold(w.Name, name) {
				clash = true
			}
		}
		// 같은 파일 안에서 다른 코드가 같은 이름을 쓰는 경우도 막는다(앞 행이 먼저 반영되므로 DB 에는 아직 없다).
		for _, o := range rows {
			if o.RowNo < r.RowNo && !strings.EqualFold(o.Cells["code"], code) && strings.EqualFold(o.Cells["name"], name) {
				clash = true
			}
		}
		switch {
		case clash:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictError, "다른 창고가 같은 이름을 쓰고 있습니다.", nil))
		case cur == nil:
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, code, domain.ImportVerdictAdd, "", nil))
		default:
			var cs []domain.ImportChange
			if cur.Name != name {
				cs = append(cs, chg("이름", cur.Name, name))
			}
			if cur.Location != loc {
				cs = append(cs, chg("위치", cur.Location, loc))
			}
			plan.Verdicts = append(plan.Verdicts, decide(r, code, cs))
		}
	}
	return plan, nil
}

func (h *ImportWarehouseHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	ws, err := h.Stock.Warehouses(ctx, tx)
	if err != nil {
		return domain.ImportApplyOutcome{}, err
	}
	for i, r := range rows {
		v := plan[i]
		if v.Verdict != domain.ImportVerdictAdd && v.Verdict != domain.ImportVerdictChange {
			continue
		}
		code := strings.ToUpper(r.Cells["code"])
		wi := domain.WarehouseInput{Code: code, Name: r.Cells["name"], Location: r.Cells["location"]}
		var before domain.Warehouse
		action := "warehouse.create"
		if v.Verdict == domain.ImportVerdictChange {
			action = "warehouse.update"
			for _, w := range ws {
				if strings.EqualFold(w.Code, code) {
					before, wi.ID = w, w.ID
				}
			}
		}
		id := wi.ID
		if id == 0 {
			if id, err = h.Stock.InsertWarehouse(ctx, tx, wi, h.now()); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
		} else if err := h.Stock.UpdateWarehouse(ctx, tx, wi, h.now()); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetWarehouse, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Detail: "창고 저장" + importSource(in), Diff: []audit.DiffItem{{Key: "code", Old: before.Code, New: code}, {Key: "name", Old: before.Name, New: wi.Name}, {Key: "location", Old: before.Location, New: wi.Location}}}); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
		ws = append(ws, domain.Warehouse{ID: id, Code: code, Name: wi.Name, Location: wi.Location, Active: true})
	}
	return domain.ImportApplyOutcome{}, nil
}

// ---- ③ 거래처 ----

// ImportVendorHandler 는 거래처 어댑터다. 키는 사업자번호(10자리), 없으면 이름이다.
type ImportVendorHandler struct {
	Base
	Repo   repository.SQLImport
	Ledger repository.SQLLedger
}

var _ domain.ImportTargetHandler = (*ImportVendorHandler)(nil)

func NewImportVendor(b Base) *ImportVendorHandler { return &ImportVendorHandler{Base: b} }

func (h *ImportVendorHandler) Target() string { return domain.ImportVendor }

// vendorIndex 는 거래처를 사업자번호·이름으로 바로 찾는 색인이다(행마다 전체를 훑지 않는다). 행에 해당하는 거래처는 사업자번호가 있으면
// 사업자번호로, 없으면 이름(공백·대소문자 무시)으로 정하고, 여럿이면 ambiguous 다.
type vendorIndex struct {
	rows []*repository.VendorRow
	biz  map[string][]*repository.VendorRow
	name map[string][]*repository.VendorRow
}

func newVendorIndex(all []repository.VendorRow) *vendorIndex {
	x := &vendorIndex{biz: map[string][]*repository.VendorRow{}, name: map[string][]*repository.VendorRow{}}
	for i := range all {
		x.add(all[i])
	}
	return x
}

func (x *vendorIndex) add(v repository.VendorRow) {
	p := &v
	x.rows = append(x.rows, p)
	if d := vendorDigits(v.BusinessNo); d != "" {
		x.biz[d] = append(x.biz[d], p)
	}
	k := vendorNameKey(v.Name)
	x.name[k] = append(x.name[k], p)
}

func (x *vendorIndex) match(biz, name string) (cur *repository.VendorRow, ambiguous bool) {
	var hits []*repository.VendorRow
	if biz != "" {
		hits = x.biz[biz]
	} else {
		hits = x.name[vendorNameKey(name)]
	}
	switch len(hits) {
	case 0:
		return nil, false
	case 1:
		return hits[0], false
	}
	return nil, true
}

func (h *ImportVendorHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	rowsAll, err := h.Repo.VendorsAll(ctx, q)
	if err != nil {
		return plan, err
	}
	all := newVendorIndex(rowsAll)
	touched := map[int64]int{}     // 파일 안에서 이미 변경 대상이 된 기존 거래처(번호) → 행 번호
	addedNames := map[string]int{} // 파일 안에서 새로 추가되는 거래처의 이름·사업자번호 → 행 번호
	for _, r := range rows {
		name, biz := r.Cells["name"], vendorDigits(r.Cells["business_no"])
		key := name
		if biz != "" {
			key = "사업자번호 " + maskTail(biz)
		}
		cur, amb := all.match(biz, name)
		if amb {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "같은 이름의 거래처가 여럿이라 변경 대상을 정할 수 없습니다. 사업자번호를 채우세요.", nil))
			continue
		}
		if cur == nil {
			// 사업자번호가 새 값인데 같은 이름의 다른 거래처가 있으면 새로 만들지 않는다(화면 등록과 같은 중복 검사).
			if biz != "" {
				if dup, _ := all.match("", name); dup != nil {
					plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, "같은 이름의 다른 거래처가 이미 있습니다. 사업자번호를 확인하세요.", nil))
					continue
				}
			}
			// 파일 안에서 같은 거래처를 사업자번호로 한 번, 이름으로 한 번 적은 경우도 같은 거래처다(입력 문자열이 아니라 이름·번호로 판정).
			dupAt := 0
			if at, ok := addedNames["n:"+vendorNameKey(name)]; ok {
				dupAt = at
			}
			if at, ok := addedNames["b:"+biz]; ok && biz != "" {
				dupAt = at
			}
			if dupAt != 0 {
				plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 거래처가 두 번 있습니다(%d번째 줄과 같은 거래처).", dupAt), nil))
				continue
			}
			addedNames["n:"+vendorNameKey(name)] = r.RowNo
			if biz != "" {
				addedNames["b:"+biz] = r.RowNo
			}
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictAdd, "", nil))
			continue
		}
		if at, dup := touched[cur.ID]; dup {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, key, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 거래처가 두 번 있습니다(%d번째 줄과 같은 거래처).", at), nil))
			continue
		}
		touched[cur.ID] = r.RowNo
		var cs []domain.ImportChange
		if vendorNameKey(cur.Name) != vendorNameKey(name) || cur.Name != name {
			cs = append(cs, chg("이름", cur.Name, name))
		}
		if days, ok := r.Cells["payment_days"]; ok {
			d, _ := strconv.Atoi(days)
			if !cur.HasTerms || cur.PaymentDays != d {
				old := "기본값"
				if cur.HasTerms {
					old = fmt.Sprintf("%d일", cur.PaymentDays)
				}
				cs = append(cs, chg("결제 조건", old, fmt.Sprintf("%d일", d)))
			}
		}
		if v, ok := r.Cells["phone"]; ok && v != cur.Phone {
			cs = append(cs, chg("전화", maskTailIf(cur.Phone), maskTail(v)))
		}
		if v, ok := r.Cells["email"]; ok && v != cur.Email {
			cs = append(cs, chg("이메일", maskEmailIf(cur.Email), maskEmail(v)))
		}
		if v, ok := r.Cells["account_no"]; ok && v != cur.AccountNo {
			cs = append(cs, chg("계좌번호", maskTailIf(cur.AccountNo), maskTail(v)))
		}
		vd := decide(r, key, cs)
		if vd.Verdict == domain.ImportVerdictChange && vendorNameKey(cur.Name) != vendorNameKey(name) && biz != "" {
			vd.Warn = "사업자번호가 같아 같은 거래처로 보고 이름을 바꿉니다."
		}
		plan.Verdicts = append(plan.Verdicts, vd)
	}
	return plan, nil
}

func maskTailIf(s string) string {
	if s == "" {
		return ""
	}
	return maskTail(s)
}

func maskEmailIf(s string) string {
	if s == "" {
		return ""
	}
	return maskEmail(s)
}

func (h *ImportVendorHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	inv := repository.SQLInventory{}
	rowsAll, err := h.Repo.VendorsAll(ctx, tx)
	if err != nil {
		return domain.ImportApplyOutcome{}, err
	}
	all := newVendorIndex(rowsAll)
	for i, r := range rows {
		v := plan[i]
		if v.Verdict != domain.ImportVerdictAdd && v.Verdict != domain.ImportVerdictChange {
			continue
		}
		name, biz := r.Cells["name"], vendorDigits(r.Cells["business_no"])
		nv := domain.Vendor{Name: name, BusinessNo: r.Cells["business_no"], AccountNo: r.Cells["account_no"], Phone: r.Cells["phone"], Email: r.Cells["email"]}
		var id int64
		action := "vendor.create"
		detail := "거래처 등록"
		var diff []audit.DiffItem
		if v.Verdict == domain.ImportVerdictAdd {
			if id, err = inv.InsertVendor(ctx, tx, nv); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			diff = []audit.DiffItem{{Key: "name", New: name}}
			all.add(repository.VendorRow{Vendor: domain.Vendor{ID: id, Name: name, BusinessNo: nv.BusinessNo}})
		} else {
			action, detail = "vendor.update", "거래처 변경"
			cur, amb := all.match(biz, name)
			if cur == nil || amb {
				return domain.ImportApplyOutcome{}, fmt.Errorf("%w: 미리보기 이후 거래처가 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
			}
			id = cur.ID
			merged := cur.Vendor
			merged.Name = name
			if biz != "" {
				merged.BusinessNo = nv.BusinessNo
			}
			if _, ok := r.Cells["phone"]; ok {
				merged.Phone = nv.Phone
			}
			if _, ok := r.Cells["email"]; ok {
				merged.Email = nv.Email
			}
			replace := false
			if a, ok := r.Cells["account_no"]; ok && a != cur.AccountNo {
				merged.AccountNo, replace = a, true
			}
			if err := h.Repo.UpdateVendor(ctx, tx, merged, replace); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if cur.Name != name {
				diff = append(diff, audit.DiffItem{Key: "name", Old: cur.Name, New: name})
			}
		}
		if days, ok := r.Cells["payment_days"]; ok {
			d, _ := strconv.Atoi(days)
			if _, err := h.Ledger.SetVendorPaymentDays(ctx, tx, id, d); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
		}
		// 사업자번호·계좌·연락처 같은 민감 값은 감사에 남기지 않는다.
		if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetVendor, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Detail: detail + importSource(in), Diff: diff}); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
	}
	return domain.ImportApplyOutcome{}, nil
}

// ---- ④ 품목 ----

// ImportItemHandler 는 품목 어댑터다. 키는 SKU(대소문자 구분 없음).
type ImportItemHandler struct {
	Base
	Repo repository.SQLImport
	Inv  repository.SQLInventory
}

var _ domain.ImportTargetHandler = (*ImportItemHandler)(nil)

func NewImportItem(b Base) *ImportItemHandler { return &ImportItemHandler{Base: b} }

func (h *ImportItemHandler) Target() string { return domain.ImportItem }

var yesNoLabel = map[bool]string{true: "예", false: "아니오"}

func (h *ImportItemHandler) Plan(ctx context.Context, q contract.Tx, rows []domain.ImportRow, in domain.ImportPlanInput) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	seenSKU := map[string]int{} // 파일 안 같은 SKU(대소문자 무시)
	itemsAll, err := h.Repo.ItemsAll(ctx, q)
	if err != nil {
		return plan, err
	}
	for _, r := range rows {
		sku, name, unit := r.Cells["sku"], r.Cells["name"], r.Cells["unit"]
		if first, dup := seenSKU[strings.ToLower(sku)]; dup {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, sku, domain.ImportVerdictError, fmt.Sprintf("파일 안에 같은 SKU 가 두 번 있습니다(%d번째 줄과 같은 SKU).", first), nil))
			continue
		}
		seenSKU[strings.ToLower(sku)] = r.RowNo
		safety := int64(0)
		if v, ok := r.Cells["safety_stock"]; ok {
			safety, _ = strconv.ParseInt(v, 10, 64)
		}
		lot := r.Cells["lot_tracked"] == "true"
		cur, found := itemsAll[strings.ToLower(sku)]
		if !found {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, sku, domain.ImportVerdictAdd, "", nil))
			continue
		}
		if cur.TrackLot && !lot && cur.OnHand > 0 {
			plan.Verdicts = append(plan.Verdicts, verdictOf(r, sku, domain.ImportVerdictError, "재고가 남은 품목은 로트 관리를 끌 수 없습니다. 로트별 재고를 잃지 않게 막습니다.", nil))
			continue
		}
		var cs []domain.ImportChange
		if cur.Name != name {
			cs = append(cs, chg("품목명", cur.Name, name))
		}
		if cur.Unit != unit {
			cs = append(cs, chg("단위", cur.Unit, unit))
		}
		if cur.SafetyStock != safety {
			cs = append(cs, chg("안전재고", fmt.Sprint(cur.SafetyStock), fmt.Sprint(safety)))
		}
		if cur.TrackLot != lot {
			cs = append(cs, chg("로트 관리", yesNoLabel[cur.TrackLot], yesNoLabel[lot]))
		}
		vd := decide(r, sku, cs)
		if !cur.Active && vd.Verdict != domain.ImportVerdictError {
			vd.Warn = "사용이 중지된 품목입니다. 값만 맞추고 사용 여부는 바꾸지 않습니다."
		}
		plan.Verdicts = append(plan.Verdicts, vd)
	}
	return plan, nil
}

func (h *ImportItemHandler) Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []domain.ImportRow, plan []domain.ImportRowVerdict, in domain.ImportPlanInput) (domain.ImportApplyOutcome, error) {
	itemsAll, err := h.Repo.ItemsAll(ctx, tx)
	if err != nil {
		return domain.ImportApplyOutcome{}, err
	}
	for i, r := range rows {
		v := plan[i]
		if v.Verdict != domain.ImportVerdictAdd && v.Verdict != domain.ImportVerdictChange {
			continue
		}
		it := domain.Item{SKU: r.Cells["sku"], Name: r.Cells["name"], Unit: r.Cells["unit"], TrackLot: r.Cells["lot_tracked"] == "true"}
		if s, ok := r.Cells["safety_stock"]; ok {
			it.SafetyStock, _ = strconv.ParseInt(s, 10, 64)
		}
		if utf8.RuneCountInString(it.Name) == 0 {
			return domain.ImportApplyOutcome{}, validation("품목명이 비어 있습니다.")
		}
		var id int64
		action := "item.create"
		var diff []audit.DiffItem
		if v.Verdict == domain.ImportVerdictAdd {
			var err error
			if id, err = h.Inv.InsertItem(ctx, tx, it); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			diff = []audit.DiffItem{{Key: "sku", New: it.SKU}, {Key: "name", New: it.Name}, {Key: "safety_stock", New: fmt.Sprint(it.SafetyStock)}}
		} else {
			cur, found := itemsAll[strings.ToLower(it.SKU)]
			if !found {
				return domain.ImportApplyOutcome{}, fmt.Errorf("%w: 미리보기 이후 품목이 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
			}
			it.ID, id, action = cur.ID, cur.ID, "item.update"
			if err := h.Repo.UpdateItem(ctx, tx, it); err != nil {
				return domain.ImportApplyOutcome{}, err
			}
			if cur.Name != it.Name {
				diff = append(diff, audit.DiffItem{Key: "name", Old: cur.Name, New: it.Name})
			}
			if cur.SafetyStock != it.SafetyStock {
				diff = append(diff, audit.DiffItem{Key: "safety_stock", Old: fmt.Sprint(cur.SafetyStock), New: fmt.Sprint(it.SafetyStock)})
			}
		}
		if err := h.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetItem, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess,
			Detail: "품목 저장" + importSource(in), Diff: diff}); err != nil {
			return domain.ImportApplyOutcome{}, err
		}
	}
	return domain.ImportApplyOutcome{}, nil
}
