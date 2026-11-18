package mcptool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 조회 자원 어댑터 규약(명세 6절)
//   (a) 호출자 context 를 그대로 서비스에 넘긴다. 어댑터는 권한을 직접 확인하지 않고(서비스가 contract.Authorize·열람 범위·법인 거름을 한다),
//   (b) 거름 조건을 서비스의 타입 있는 필터 구조체로 바꾼다(문자열 SQL 을 만들지 않는다),
//   (c) 건수 상한과 정렬 칸 허용 목록을 강제하고(Resource.MaxLimit·FieldSpec.Sortable),
//   (d) 반환 칸은 서비스가 이미 가린 값을 복사만 한다. 개인정보 원문 칸(연락처·계좌·사업자번호·이메일)은 어떤 자원에도 없다.
// 서비스가 네이티브로 거르지 못하는 칸(품목 이름 접두 등)은 서비스가 돌려준 결과를 더 좁히는 후처리만 한다(결과를 넓히지 않는다).

var kst = time.FixedZone("KST", 9*3600)

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(kst).Format("2006-01-02")
}

func eqOnly() []string    { return []string{OpEq} }
func rangeOps() []string  { return []string{OpGte, OpLte} }
func prefixOps() []string { return []string{OpEq, OpPrefix} }
func errNotWired(n string) error {
	return fmt.Errorf("%w: %s 를 읽을 서비스가 연결되어 있지 않습니다", contract.ErrInvalidState, n)
}

// condValue 는 질의에서 칸의 조건을 찾는다.
func (q Query) cond(field, op string) (Cond, bool) {
	for _, c := range q.Conds {
		if c.Field == field && (op == "" || c.Op == op) {
			return c, true
		}
	}
	return Cond{}, false
}

func (q Query) str(field string) string {
	if c, ok := q.cond(field, OpEq); ok {
		s, _ := c.Value.(string)
		return s
	}
	return ""
}

func (q Query) i64(field string) int64 {
	if c, ok := q.cond(field, OpEq); ok {
		n, _ := c.Value.(int64)
		return n
	}
	return 0
}

func (q Query) dateRange(field string) (from, to string) {
	if c, ok := q.cond(field, OpGte); ok {
		from, _ = c.Value.(string)
	}
	if c, ok := q.cond(field, OpLte); ok {
		to, _ = c.Value.(string)
	}
	return
}

// narrow 는 문자열 칸 조건(eq·prefix)을 후처리로 적용한다(대소문자 무시).
func (q Query) narrow(field, value string) bool {
	for _, c := range q.Conds {
		if c.Field != field {
			continue
		}
		want, _ := c.Value.(string)
		v, w := strings.ToLower(value), strings.ToLower(want)
		switch c.Op {
		case OpEq:
			if v != w {
				return false
			}
		case OpPrefix:
			if !strings.HasPrefix(v, w) {
				return false
			}
		}
	}
	return true
}

func sortRows(rows []map[string]any, field, dir string) {
	if field == "" {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i][field], rows[j][field]
		less := false
		switch x := a.(type) {
		case int64:
			y, _ := b.(int64)
			less = x < y
		case string:
			y, _ := b.(string)
			less = x < y
		}
		if dir == "desc" {
			return !less && a != b
		}
		return less
	})
}

// BuiltinCatalog 는 1차 조회 자원 8개를 서비스에 연결한 카탈로그다(명세 6절). 서비스가 비어 있으면 그 자원의 List 는 오류를 돌려준다.
func BuiltinCatalog(d deps.Deps) *Catalog {
	return NewCatalog(
		purchaseRequests(d), purchaseOrders(d), items(d), stockAlerts(d), payables(d), journals(d), taxInvoices(d), approvalDocs(d),
	).WithDrafts(BuiltinDrafts(d)...)
}

var prStatuses = []string{"draft", "submitted", "in_review", "rejected", "approved", "ordered"}

var prStatusLabels = map[string]string{"draft": "임시저장", "submitted": "상신", "in_review": "결재 중", "rejected": "반려", "approved": "승인", "ordered": "발주됨"}

func purchaseRequests(d deps.Deps) *Resource {
	return &Resource{
		Name: "purchase_requests", Label: "구매요청", Perm: contract.PermPurchaseRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		DefaultSort: "created_at", DefaultDir: "desc",
		Fields: []FieldSpec{
			{Name: "no", Label: "번호", Type: TypeString, Return: true},
			{Name: "title", Label: "제목", Type: TypeString, Return: true, Class: ClassFreeText},
			{Name: "status", Label: "상태", Type: TypeEnum, Values: prStatuses, Labels: prStatusLabels, Ops: eqOnly(), Return: true},
			{Name: "total", Label: "합계(원)", Type: TypeInt, Return: true, Sortable: true},
			{Name: "created_at", Label: "작성일", Type: TypeDate, Ops: rangeOps(), Return: true, Sortable: true, Ctx: []string{CtxToday}},
			{Name: "requester", Label: "작성자", Type: TypeString, Return: true},
			{Name: "requester_id", Label: "작성자 번호", Type: TypeInt, Ops: eqOnly(), Ctx: []string{CtxMyUser}},
			{Name: "department_id", Label: "부서 번호", Type: TypeInt, Ops: eqOnly(), Return: true, Ctx: []string{CtxMyDepartment}},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Purchase == nil {
				return nil, errNotWired("구매요청")
			}
			from, to := q.dateRange("created_at")
			sortCol := ""
			switch q.Sort {
			case "created_at":
				sortCol = "created_at"
			case "total":
				sortCol = "total_amount"
			}
			recs, err := d.Purchase.List(ctx, domain.PRFilter{Status: domain.PRStatus(q.str("status")), DepartmentID: q.i64("department_id"), RequesterID: q.i64("requester_id"),
				Limit: q.Limit, From: from, To: to, Sort: sortCol, Order: q.Dir})
			if err != nil {
				return nil, err
			}
			rows := make([]map[string]any, 0, len(recs))
			for _, r := range recs {
				rows = append(rows, map[string]any{"no": r.No, "title": r.Title, "status": string(r.Status), "total": r.TotalAmount, "created_at": day(r.CreatedAt),
					"requester": r.RequesterName, "department_id": r.DepartmentID})
			}
			return rows, nil
		},
	}
}

func purchaseOrders(d deps.Deps) *Resource {
	return &Resource{
		Name: "purchase_orders", Label: "발주", Perm: contract.PermInventoryRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "no", Label: "번호", Type: TypeString, Return: true},
			{Name: "status", Label: "상태", Type: TypeEnum, Values: []string{"open", "partial", "received"}, Labels: map[string]string{"open": "발주됨", "partial": "일부 입고", "received": "입고 완료"}, Ops: eqOnly(), Return: true},
			{Name: "total", Label: "합계(원)", Type: TypeInt, Return: true},
			{Name: "created_at", Label: "발주일", Type: TypeDate, Return: true},
			{Name: "due_date", Label: "납기일", Type: TypeDate, Return: true},
			{Name: "delivery_place", Label: "납품 장소", Type: TypeString, Return: true, Class: ClassFreeText},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Inventory == nil {
				return nil, errNotWired("발주")
			}
			pos, err := d.Inventory.ListPurchaseOrders(ctx, domain.POFilter{Status: domain.POStatus(q.str("status")), Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			rows := make([]map[string]any, 0, len(pos))
			for _, p := range pos {
				rows = append(rows, map[string]any{"no": p.No, "status": string(p.Status), "total": p.TotalAmount, "created_at": day(p.CreatedAt), "due_date": p.DueDate, "delivery_place": p.DeliveryPlace})
			}
			return rows, nil
		},
	}
}

func items(d deps.Deps) *Resource {
	return &Resource{
		Name: "items", Label: "품목", Perm: contract.PermInventoryRead, EntityOwned: false, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "sku", Label: "품목 코드", Type: TypeString, Ops: prefixOps(), Return: true},
			{Name: "name", Label: "품목 이름", Type: TypeString, Ops: prefixOps(), Return: true, Class: ClassFreeText},
			{Name: "unit", Label: "단위", Type: TypeString, Return: true},
			{Name: "safety_stock", Label: "안전재고", Type: TypeInt, Return: true},
			{Name: "on_hand", Label: "현재고", Type: TypeInt, Return: true},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Inventory == nil {
				return nil, errNotWired("품목")
			}
			// 서비스의 검색은 "포함" 이다. 이름·코드 접두 조건은 첫 값으로 넓게 가져와 접두·일치로 좁힌다.
			term, narrowing := "", false
			for _, c := range q.Conds {
				if c.Field == "name" || c.Field == "sku" {
					term, _ = c.Value.(string)
					narrowing = true
					break
				}
			}
			fetch := q.Limit
			if narrowing {
				fetch = 200
			}
			found, err := d.Inventory.SearchItems(ctx, term, fetch)
			if err != nil {
				return nil, err
			}
			wantStock := false
			for _, f := range q.Fields {
				if f == "on_hand" {
					wantStock = true
				}
			}
			rows := []map[string]any{}
			for _, it := range found {
				if !q.narrow("name", it.Name) || !q.narrow("sku", it.SKU) {
					continue
				}
				row := map[string]any{"sku": it.SKU, "name": it.Name, "unit": it.Unit, "safety_stock": it.SafetyStock}
				if wantStock {
					st, err := d.Inventory.GetStock(ctx, it.ID)
					if err != nil {
						return nil, err
					}
					row["on_hand"] = st.OnHand
				}
				rows = append(rows, row)
				if len(rows) >= q.Limit {
					break
				}
			}
			return rows, nil
		},
	}
}

func stockAlerts(d deps.Deps) *Resource {
	return &Resource{
		Name: "stock_alerts", Label: "재고 경고", Perm: contract.PermInventoryRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "kind", Label: "종류", Type: TypeEnum, Values: []string{"below_safety", "expiring"}, Labels: map[string]string{"below_safety": "안전재고 미달", "expiring": "유효기한 임박"}, Ops: eqOnly(), Return: true},
			{Name: "sku", Label: "품목 코드", Type: TypeString, Return: true},
			{Name: "name", Label: "품목 이름", Type: TypeString, Return: true, Class: ClassFreeText},
			{Name: "on_hand", Label: "현재고", Type: TypeInt, Return: true},
			{Name: "safety_stock", Label: "안전재고", Type: TypeInt, Return: true},
			{Name: "shortage", Label: "부족량", Type: TypeInt, Return: true},
			{Name: "lot_no", Label: "로트 번호", Type: TypeString, Return: true},
			{Name: "lot_qty", Label: "로트 수량", Type: TypeInt, Return: true},
			{Name: "expiry_date", Label: "유효기한", Type: TypeDate, Return: true},
			{Name: "days_to_expire", Label: "남은 일수", Type: TypeInt, Return: true},
			{Name: "within_days", Label: "임박 기준 일수", Type: TypeInt, Ops: eqOnly()},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Inventory == nil {
				return nil, errNotWired("재고 경고")
			}
			rows := []map[string]any{}
			if q.str("kind") == "expiring" {
				days := int(q.i64("within_days"))
				if days <= 0 {
					days = 30
				}
				if days > 365 {
					days = 365
				}
				lots, err := d.Inventory.ListExpiring(ctx, days)
				if err != nil {
					return nil, err
				}
				for _, l := range lots {
					rows = append(rows, map[string]any{"kind": "expiring", "sku": l.Item.SKU, "name": l.Item.Name, "lot_no": l.Lot.LotNo, "lot_qty": l.Lot.Quantity,
						"expiry_date": l.Lot.ExpiryDate, "days_to_expire": int64(l.DaysToExpire)})
				}
				return rows, nil
			}
			low, err := d.Inventory.ListBelowSafety(ctx)
			if err != nil {
				return nil, err
			}
			for _, s := range low {
				short := s.Item.SafetyStock - s.OnHand
				if short < 0 {
					short = 0
				}
				rows = append(rows, map[string]any{"kind": "below_safety", "sku": s.Item.SKU, "name": s.Item.Name, "on_hand": s.OnHand, "safety_stock": s.Item.SafetyStock, "shortage": short})
			}
			return rows, nil
		},
	}
}

func payables(d deps.Deps) *Resource {
	return &Resource{
		Name: "payables", Label: "미지급(채무)", Perm: contract.PermLedgerRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		DefaultSort: "outstanding", DefaultDir: "desc",
		Fields: []FieldSpec{
			{Name: "vendor_name", Label: "거래처", Type: TypeString, Ops: prefixOps(), Return: true, Class: ClassFreeText},
			{Name: "count", Label: "미지급 건수", Type: TypeInt, Return: true},
			{Name: "outstanding", Label: "미지급 합계(원)", Type: TypeInt, Return: true, Sortable: true},
			{Name: "overdue", Label: "만기 지난 금액(원)", Type: TypeInt, Return: true, Sortable: true},
			{Name: "earliest_due", Label: "가장 이른 만기", Type: TypeDate, Return: true},
			{Name: "payment_days", Label: "지급 기한(일)", Type: TypeInt, Return: true},
			{Name: "as_of", Label: "기준일", Type: TypeDate, Ops: eqOnly(), Ctx: []string{CtxToday}},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Payables == nil {
				return nil, errNotWired("미지급")
			}
			rep, err := d.Payables.ByVendor(ctx, q.str("as_of"))
			if err != nil {
				return nil, err
			}
			rows := []map[string]any{}
			for _, r := range rep.Rows {
				if !q.narrow("vendor_name", r.VendorName) {
					continue
				}
				rows = append(rows, map[string]any{"vendor_name": r.VendorName, "count": int64(r.Count), "outstanding": r.Outstanding, "overdue": r.OverdueAmount,
					"earliest_due": r.EarliestDue, "payment_days": int64(r.PaymentDays)})
			}
			sortRows(rows, q.Sort, q.Dir)
			return rows, nil
		},
	}
}

func journals(d deps.Deps) *Resource {
	return &Resource{
		Name: "journals", Label: "전표", Perm: contract.PermLedgerRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "no", Label: "번호", Type: TypeString, Return: true},
			{Name: "status", Label: "상태", Type: TypeEnum, Values: []string{"draft", "requested", "approved", "canceled"}, Labels: map[string]string{"draft": "임시", "requested": "승인 요청", "approved": "승인", "canceled": "취소"}, Ops: eqOnly(), Return: true},
			{Name: "kind", Label: "종류", Type: TypeEnum, Values: []string{"general", "auto_purchase", "auto_expense", "auto_payment", "auto_card", "auto_aicost", "reversal", "opening"}, Labels: map[string]string{"general": "일반", "auto_purchase": "매입 자동", "auto_expense": "비용 자동", "auto_payment": "지급 자동", "auto_card": "카드 자동", "reversal": "역분개", "opening": "기초"}, Ops: eqOnly(), Return: true},
			{Name: "posting_date", Label: "전기일", Type: TypeDate, Ops: rangeOps(), Return: true, Ctx: []string{CtxToday}},
			{Name: "total", Label: "합계(원)", Type: TypeInt, Return: true},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Journals == nil {
				return nil, errNotWired("전표")
			}
			from, to := q.dateRange("posting_date")
			page, err := d.Journals.List(ctx, domain.JournalFilter{Status: q.str("status"), Kind: q.str("kind"), From: from, To: to, Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			rows := make([]map[string]any, 0, len(page.Rows))
			for _, j := range page.Rows {
				rows = append(rows, map[string]any{"no": j.No, "status": j.Status, "kind": j.Kind, "posting_date": j.PostingDate, "total": j.TotalAmount})
			}
			return rows, nil
		},
	}
}

func taxInvoices(d deps.Deps) *Resource {
	return &Resource{
		Name: "tax_invoices", Label: "전자세금계산서", Perm: contract.PermTaxInvoiceRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "no", Label: "번호", Type: TypeString, Return: true},
			{Name: "direction", Label: "방향", Type: TypeEnum, Values: []string{"sale", "purchase"}, Labels: map[string]string{"sale": "매출", "purchase": "매입"}, Ops: eqOnly(), Return: true},
			{Name: "status", Label: "상태", Type: TypeEnum, Values: []string{"draft", "issued", "sent", "accepted", "failed", "canceled"}, Labels: map[string]string{"draft": "작성 중", "issued": "발행 대기", "sent": "전송됨", "accepted": "국세청 수신", "failed": "실패", "canceled": "취소"}, Ops: eqOnly(), Return: true},
			{Name: "issue_date", Label: "작성일", Type: TypeDate, Ops: rangeOps(), Return: true, Ctx: []string{CtxToday}},
			{Name: "counterparty", Label: "상대방", Type: TypeString, Ops: prefixOps(), Return: true, Class: ClassFreeText},
			{Name: "supply_total", Label: "공급가액(원)", Type: TypeInt, Return: true},
			{Name: "tax_total", Label: "세액(원)", Type: TypeInt, Return: true},
			{Name: "total", Label: "합계(원)", Type: TypeInt, Return: true},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.TaxInvoice == nil {
				return nil, errNotWired("전자세금계산서")
			}
			from, to := q.dateRange("issue_date")
			hint := ""
			narrowing := false
			if c, ok := q.cond("counterparty", ""); ok {
				hint, _ = c.Value.(string)
				narrowing = true
			}
			// 상대방 접두는 서비스의 "포함" 검색 결과를 후처리로 좁힌다. 서비스 쪽 한 쪽 상한(200)이 접두 일치를 가리지 않도록
			// 일치가 충분히 모일 때까지 쪽을 넘겨 가며 가져온다(최대 5쪽 = 1,000건, 그 뒤는 더 구체적인 접두를 쓰도록 한계로 둔다).
			pageSize, maxPages := q.Limit, 1
			if narrowing {
				pageSize, maxPages = 200, 5
			}
			rows := []map[string]any{}
			for pg := 0; pg < maxPages; pg++ {
				page, err := d.TaxInvoice.List(ctx, domain.TaxInvoiceFilter{Direction: q.str("direction"), Status: q.str("status"), From: from, To: to, Counterparty: hint, Limit: pageSize, Offset: pg * pageSize})
				if err != nil {
					return nil, err
				}
				for _, t := range page.Rows {
					name := t.Buyer.Name
					if t.Direction == domain.TaxPurchase {
						name = t.Supplier.Name
					}
					if !q.narrow("counterparty", name) {
						continue
					}
					rows = append(rows, map[string]any{"no": t.No, "direction": t.Direction, "status": t.Status, "issue_date": t.IssueDate, "counterparty": name,
						"supply_total": t.SupplyTotal, "tax_total": t.TaxTotal, "total": t.Total})
				}
				if len(rows) >= q.Limit || len(page.Rows) < pageSize || (pg+1)*pageSize >= page.Total {
					break
				}
			}
			return rows, nil
		},
	}
}

func approvalDocs(d deps.Deps) *Resource {
	return &Resource{
		Name: "approval_docs", Label: "결재 문서(양식)", Perm: contract.PermDraftRead, EntityOwned: true, DefaultLimit: 20, MaxLimit: 50, Version: 1,
		Fields: []FieldSpec{
			{Name: "no", Label: "번호", Type: TypeString, Return: true},
			{Name: "form_code", Label: "양식 코드", Type: TypeString, Ops: eqOnly(), Return: true},
			{Name: "form_name", Label: "양식", Type: TypeString, Return: true},
			{Name: "title", Label: "제목", Type: TypeString, Return: true, Class: ClassFreeText},
			{Name: "status", Label: "상태", Type: TypeEnum, Values: prStatuses, Labels: prStatusLabels, Ops: eqOnly(), Return: true},
			{Name: "created_at", Label: "기안일", Type: TypeDate, Return: true},
			{Name: "requester", Label: "기안자", Type: TypeString, Return: true},
			{Name: "requester_id", Label: "기안자 번호", Type: TypeInt, Ops: eqOnly(), Ctx: []string{CtxMyUser}},
			{Name: "amount", Label: "금액(원)", Type: TypeInt, Return: true},
		},
		List: func(ctx context.Context, q Query) ([]map[string]any, error) {
			if d.Docs == nil {
				return nil, errNotWired("결재 문서")
			}
			recs, err := d.Docs.List(ctx, domain.DocFilter{FormCode: q.str("form_code"), Status: domain.PRStatus(q.str("status")), RequesterID: q.i64("requester_id"), Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			rows := make([]map[string]any, 0, len(recs))
			for _, r := range recs {
				rows = append(rows, map[string]any{"no": r.No, "form_code": r.FormCode, "form_name": r.FormName, "title": r.Title, "status": string(r.Status),
					"created_at": day(r.CreatedAt), "requester": r.RequesterName, "amount": r.Amount})
			}
			return rows, nil
		},
	}
}
