// Package demo 는 설치 마법사에서 "데모 데이터"를 고른 때만 넣는 고정 샘플 업무 세트를 만든다(B38, 명세 38 4.6).
//
// 원칙
//   - 모든 데이터는 서비스 경로(조직 발령·구매요청·결재·입고·출고·전표)로 만든다. 직접 INSERT 로 감사 체인과 원장을 건너뛰지 않으므로
//     감사 기록·재고 원장·전표 차대·채무가 실제 운영 흐름과 똑같이 정합하다.
//   - 결정적이다: 같은 입력(같은 시계)이면 같은 구조의 데이터가 나온다. 날짜는 시계 기준 상대값이다.
//   - 데모 사용자는 비밀번호·외부 연결 없이 만들어진다(표시용). 이메일은 예약 도메인(@demo.invalid)이라 외부 로그인도 만날 수 없다.
//   - 개발 시드(org.SeedDev, @pl.test)와 코드·이메일이 겹치지 않는다. 조직 이벤트가 하나도 없는 DB 에서만 들어간다.
//   - 서비스가 자기 트랜잭션을 열므로 설치 완료 트랜잭션이 끝난 뒤 호출한다(SQLite 는 연결이 하나라 트랜잭션 안에서는 부를 수 없다).
package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/service"
)

const settingKey = "demo_seed"

// Seed 는 데모 업무 세트를 만든다. 한 DB 에 한 번만 된다(두 번째 호출은 오류).
func Seed(ctx context.Context, d deps.Deps) error {
	osvc, ok := d.Org.(*org.Service)
	if !ok {
		return errors.New("조직 서비스가 없어 데모 데이터를 넣을 수 없습니다")
	}
	if err := claim(ctx, d.DB); err != nil {
		return err
	}
	dorg, err := osvc.SeedDemoOrg(ctx)
	if err != nil {
		return err
	}
	w := &world{d: d, org: dorg, ctx: ctx}
	if err := w.principals(); err != nil {
		return err
	}
	for _, step := range []func() error{w.masters, w.purchases, w.stockIssue, w.journals} {
		if err := step(); err != nil {
			return err
		}
	}
	_, err = d.DB.ExecContext(ctx, `UPDATE app_settings SET value = 'done' WHERE key = ?`, settingKey)
	return err
}

// AdminUnit·AdminRank 는 데모 데이터를 고른 설치에서 첫 관리자가 배치되는 조직 코드(경영지원팀)와 직위다(조직장은 아니다).
const (
	AdminUnit = "DEP-MG"
	AdminRank = "부장"
)

// PlaceAdmin 은 첫 관리자를 데모 조직(경영지원팀)에 배치한다. org 의 발령 경로(PlaceUser)를 쓰므로 발령 이력·감사가 남고,
// 관리자 본인 이름으로 등록해 위험 권한 획득 판정도 거친다. 멱등이다(이미 배치됐으면 false).
func PlaceAdmin(ctx context.Context, d deps.Deps, adminID int64) (bool, error) {
	osvc, ok := d.Org.(*org.Service)
	if !ok {
		return false, errors.New("조직 서비스가 없어 첫 관리자를 배치할 수 없습니다")
	}
	var name, role string
	var dept sql.NullInt64
	if err := d.DB.QueryRowContext(ctx, `SELECT name, role, department_id FROM users WHERE id = ?`, adminID).Scan(&name, &role, &dept); err != nil {
		return false, fmt.Errorf("첫 관리자를 찾을 수 없습니다: %w", err)
	}
	actor := contract.Principal{UserID: adminID, Name: name, Role: contract.Role(role), DepartmentID: dept.Int64, Channel: contract.ChannelWeb}
	return osvc.PlaceUser(ctx, actor, adminID, AdminUnit, AdminRank)
}

// SeedForInstall 은 설치 마법사의 데모 훅이다: 데모 세트를 넣고 첫 관리자를 배치한다. 세트 실패는 오류로 돌려준다(마법사가 안내).
// 배치 실패는 설치도 데모 세트도 되돌리지 않고 로그로만 안내한다(관리자는 부서만 있는 채로 남고, erp-admin demo-seed 로 다시 배치한다).
func SeedForInstall(ctx context.Context, d deps.Deps, adminID int64) error {
	if err := Seed(ctx, d); err != nil {
		return err
	}
	if _, err := PlaceAdmin(ctx, d, adminID); err != nil {
		log.Printf("setup: 데모 데이터는 넣었지만 첫 관리자를 조직에 배치하지 못했습니다(erp-admin demo-seed 로 다시 배치할 수 있습니다): %v", err)
	}
	return nil
}

// claim 은 데모 세트를 한 번만 넣도록 표시한다.
func claim(ctx context.Context, db *sql.DB) error {
	res, err := db.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, 'running') ON CONFLICT DO NOTHING`, settingKey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("데모 데이터는 이미 들어 있거나 넣는 중입니다")
	}
	return nil
}

type world struct {
	d    deps.Deps
	org  org.DemoOrg
	ctx  context.Context
	who  map[string]contract.Principal
	inv  *service.InventoryService
	vend map[string]domain.Vendor
	item map[string]domain.Item
	dept map[string]int64 // 조직 코드 → 호환 계층 부서 번호
}

func (w *world) as(key string) context.Context {
	p, ok := w.who[key]
	if !ok {
		panic("데모 사람 키가 없음: " + key)
	}
	return contract.WithPrincipal(w.ctx, p)
}

// principals 는 만든 사람들의 Principal(역할 기본 권한표로 판정)을 준비한다.
func (w *world) principals() error {
	w.who = map[string]contract.Principal{}
	for _, p := range org.DemoPeople {
		var dept sql.NullInt64
		if err := w.d.DB.QueryRowContext(w.ctx, `SELECT department_id FROM users WHERE id = ?`, w.org.Users[p.Key]).Scan(&dept); err != nil {
			return err
		}
		w.who[p.Key] = contract.Principal{UserID: w.org.Users[p.Key], Name: p.Name, Role: p.Role, DepartmentID: dept.Int64, Channel: contract.ChannelWeb}
	}
	w.dept = map[string]int64{}
	for code := range w.org.Units {
		w.dept[code] = w.org.Units[code]
	}
	base := service.Base{DB: w.d.DB, Txs: w.d.Txs, Audit: w.d.Audit, Masker: w.d.Masker, Org: w.d.Org, Privacy: w.d.Privacy, Now: time.Now}
	w.inv = service.NewInventoryService(base)
	return nil
}

func (w *world) masters() error {
	w.vend = map[string]domain.Vendor{}
	w.item = map[string]domain.Item{}
	for _, v := range []domain.Vendor{
		{Name: "한빛상사", BusinessNo: "000-00-00001", Phone: "02-555-0101", Email: "sales@hanbit.demo.invalid"},
		{Name: "대성전자", BusinessNo: "000-00-00002", Phone: "02-555-0102", Email: "order@daesung.demo.invalid"},
		{Name: "미래사무기기", BusinessNo: "000-00-00003", Phone: "02-555-0103", Email: "mirae@office.demo.invalid"},
	} {
		got, err := w.inv.CreateVendor(w.as("finance.a"), v)
		if err != nil {
			return fmt.Errorf("데모 거래처 %s: %w", v.Name, err)
		}
		w.vend[v.Name] = got
	}
	for _, it := range []service.ItemInput{
		{SKU: "DEMO-MON27", Name: "모니터 27형", Unit: "EA", SafetyStock: 3},
		{SKU: "DEMO-PAPER", Name: "복사용지 A4", Unit: "박스", SafetyStock: 20},
		{SKU: "DEMO-KEYBD", Name: "무선 키보드", Unit: "EA", SafetyStock: 5},
		{SKU: "DEMO-TONER", Name: "토너 카트리지", Unit: "EA", SafetyStock: 2, TrackLot: true},
	} {
		got, err := w.inv.CreateItem(w.as("finance.a"), it)
		if err != nil {
			return fmt.Errorf("데모 품목 %s: %w", it.SKU, err)
		}
		w.item[it.SKU] = got
	}
	return nil
}

// approveAll 은 결재 차례가 된 사람을 순서대로 찾아 승인한다(결재선은 양식·금액이 정하므로 후보 순서로 시도한다).
// 최종 승인(발주 생성)에 이르면 그 결과를 돌려준다.
func (w *world) approveAll(prID int64, order ...string) (*domain.PurchaseOrder, error) {
	for round := 0; round < len(order)+1; round++ {
		progressed := false
		for _, who := range order {
			res, err := w.d.Approval.Decide(w.as(who), prID, domain.Decision{Approve: true, Comment: "데모 승인"})
			if err != nil {
				continue // 아직 이 사람의 차례가 아니다
			}
			progressed = true
			if res.PurchaseOrder != nil {
				return res.PurchaseOrder, nil
			}
		}
		if !progressed {
			break
		}
	}
	return nil, fmt.Errorf("구매요청 %d 의 결재가 끝나지 않았습니다", prID)
}

var purchaseApprovers = []string{"purchase.lead", "purchase.dep", "finance.lead", "pl.div", "mg.div", "ceo"}

func (w *world) draft(by, vendor, title string, lines ...domain.PRLineInput) (domain.PurchaseRequest, error) {
	return w.d.Purchase.CreateDraft(w.as(by), domain.DraftInput{VendorID: w.vend[vendor].ID, Title: title, Reason: "데모 데이터", Lines: lines})
}

func (w *world) order(by, vendor, title string, lines ...domain.PRLineInput) (domain.PurchaseOrder, error) {
	pr, err := w.draft(by, vendor, title, lines...)
	if err != nil {
		return domain.PurchaseOrder{}, fmt.Errorf("%s 작성: %w", title, err)
	}
	if _, err := w.d.Purchase.Submit(w.as(by), pr.ID); err != nil {
		return domain.PurchaseOrder{}, fmt.Errorf("%s 상신: %w", title, err)
	}
	po, err := w.approveAll(pr.ID, purchaseApprovers...)
	if err != nil {
		return domain.PurchaseOrder{}, fmt.Errorf("%s: %w", title, err)
	}
	return *po, nil
}

func (w *world) receive(po domain.PurchaseOrder, qty int64, lot, expiry string) error {
	_, err := w.d.Inventory.Receive(w.as("finance.a"), domain.ReceiveInput{POID: po.ID, Lines: []domain.ReceiptLine{
		{POLineID: po.Lines[0].ID, ItemID: po.Lines[0].ItemID, Quantity: qty, LotNo: lot, ExpiryDate: expiry}}})
	return err
}

func (w *world) purchases() error {
	today := time.Now()
	// 1) 모니터: 전량 입고(두 번에 나눠).
	po1, err := w.order("purchase.staff1", "대성전자", "모니터 구매", domain.PRLineInput{ItemID: w.item["DEMO-MON27"].ID, Quantity: 10, UnitPrice: 300_000})
	if err != nil {
		return err
	}
	if err := w.receive(po1, 6, "", ""); err != nil {
		return fmt.Errorf("모니터 1차 입고: %w", err)
	}
	if err := w.receive(po1, 4, "", ""); err != nil {
		return fmt.Errorf("모니터 2차 입고: %w", err)
	}
	// 2) 복사용지: 부분 입고.
	po2, err := w.order("purchase.staff2", "미래사무기기", "복사용지 구매", domain.PRLineInput{ItemID: w.item["DEMO-PAPER"].ID, Quantity: 50, UnitPrice: 20_000})
	if err != nil {
		return err
	}
	if err := w.receive(po2, 30, "", ""); err != nil {
		return fmt.Errorf("복사용지 입고: %w", err)
	}
	// 3) 토너: 로트 관리 품목 입고(로트 번호·유효기한).
	po3, err := w.order("purchase.staff1", "미래사무기기", "토너 구매", domain.PRLineInput{ItemID: w.item["DEMO-TONER"].ID, Quantity: 5, UnitPrice: 80_000})
	if err != nil {
		return err
	}
	if err := w.receive(po3, 5, "T-DEMO-01", today.AddDate(0, 12, 0).Format("2006-01-02")); err != nil {
		return fmt.Errorf("토너 입고: %w", err)
	}
	// 4) 결재 진행 중: 팀장만 승인한 상태로 둔다.
	pr4, err := w.draft("purchase.staff1", "한빛상사", "키보드 구매", domain.PRLineInput{ItemID: w.item["DEMO-KEYBD"].ID, Quantity: 20, UnitPrice: 50_000})
	if err != nil {
		return err
	}
	if _, err := w.d.Purchase.Submit(w.as("purchase.staff1"), pr4.ID); err != nil {
		return fmt.Errorf("키보드 상신: %w", err)
	}
	if _, err := w.d.Approval.Decide(w.as("purchase.lead"), pr4.ID, domain.Decision{Approve: true, Comment: "데모 승인"}); err != nil {
		return fmt.Errorf("키보드 팀장 승인: %w", err)
	}
	// 5) 반려.
	pr5, err := w.draft("purchase.staff2", "대성전자", "모니터 추가 구매", domain.PRLineInput{ItemID: w.item["DEMO-MON27"].ID, Quantity: 5, UnitPrice: 310_000})
	if err != nil {
		return err
	}
	if _, err := w.d.Purchase.Submit(w.as("purchase.staff2"), pr5.ID); err != nil {
		return fmt.Errorf("추가 구매 상신: %w", err)
	}
	if _, err := w.d.Approval.Decide(w.as("purchase.lead"), pr5.ID, domain.Decision{Approve: false, Comment: "이번 분기 예산에서 제외합니다"}); err != nil {
		return fmt.Errorf("추가 구매 반려: %w", err)
	}
	// 6) 임시저장.
	if _, err := w.draft("purchase.staff2", "한빛상사", "복사용지 추가 구매(작성 중)", domain.PRLineInput{ItemID: w.item["DEMO-PAPER"].ID, Quantity: 30, UnitPrice: 20_000}); err != nil {
		return err
	}
	return nil
}

// stockIssue 는 영업팀에 모니터 2대를 출고한다(선입선출 배분, 재고 원장에 출고 줄이 남는다).
func (w *world) stockIssue() error {
	var wh int64
	if err := w.d.DB.QueryRowContext(w.ctx, `SELECT id FROM warehouses WHERE active = 1 ORDER BY id LIMIT 1`).Scan(&wh); err != nil {
		return fmt.Errorf("기본 창고를 찾을 수 없습니다: %w", err)
	}
	_, err := w.d.StockIssue.Issue(w.as("finance.a"), domain.IssueInput{
		DepartmentID: w.deptID("sales.staff"), WarehouseID: wh, Method: domain.IssueFIFO, Memo: "데모 출고",
		Lines: []domain.IssueLineInput{{ItemID: w.item["DEMO-MON27"].ID, Qty: 2}}})
	if err != nil {
		return fmt.Errorf("데모 출고: %w", err)
	}
	return nil
}

func (w *world) deptID(key string) int64 { return w.who[key].DepartmentID }

// journals 는 모니터 매입 전표를 입력·승인해 채무(한빛이 아닌 대성전자 300만 원)가 생기게 한다.
func (w *world) journals() error {
	today := time.Now()
	j, err := w.d.Journals.Create(w.as("finance.a"), domain.JournalInput{
		PostingDate: today.Format("2006-01-02"), TradeDate: today.AddDate(0, 0, -5).Format("2006-01-02"), Memo: "모니터 매입(데모)",
		Lines: []domain.JournalLineInput{
			{AccountCode: "1210", Side: "D", Amount: 3_000_000},
			{AccountCode: "2110", Side: "C", Amount: 3_000_000, VendorID: w.vend["대성전자"].ID},
		}})
	if err != nil {
		return fmt.Errorf("데모 전표 입력: %w", err)
	}
	j, err = w.d.Journals.RequestApproval(w.as("finance.a"), j.No)
	if err != nil {
		return fmt.Errorf("데모 전표 승인 요청: %w", err)
	}
	for round := 0; round < 4; round++ {
		for _, who := range []string{"finance.lead", "mg.dep", "mg.div", "ceo"} {
			if _, err := w.d.Approval.Decide(w.as(who), j.DocID, domain.Decision{Approve: true, Comment: "데모 승인"}); err != nil {
				continue
			}
		}
		got, err := w.d.Journals.Get(w.as("finance.a"), j.No)
		if err != nil {
			return err
		}
		if got.Status == domain.JournalApproved {
			return nil
		}
	}
	return errors.New("데모 전표 승인이 끝나지 않았습니다")
}
