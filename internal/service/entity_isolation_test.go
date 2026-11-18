package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// B44 2단계(erp): 법인이 둘일 때 업무 데이터가 법인 안에서만 읽히고 쓰이며, 번호는 법인별로 올라가고 기본 법인이 아닌 법인의 번호에는 법인 코드가 붙는다.

func (e *env) secondEntity(t *testing.T) int64 {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO entities (code, name, business_no, is_default, active, version, created_at, updated_at) VALUES ('ABC', '계열사', '2208162517', 0, 1, 1, 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM entities WHERE code = 'ABC'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// inEntity 는 사용자의 요청을 그 법인의 현재 법인 context 로 만든다. entityID 0 은 전체 법인 모드다.
func inEntity(user int64, entityID int64) context.Context {
	if entityID == 0 {
		return contract.WithActiveEntity(as(user), contract.ActiveEntity{All: true})
	}
	return contract.WithActiveEntity(as(user), contract.ActiveEntity{ID: entityID})
}

func (e *env) jvIn(t *testing.T, ctx context.Context, memo string) domain.Journal {
	t.Helper()
	j, err := e.journals.Create(ctx, domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: memo, Lines: []domain.JournalLineInput{
		{AccountCode: "1210", Side: "D", Amount: 5000}, {AccountCode: "1310", Side: "C", Amount: 5000}}})
	if err != nil {
		t.Fatalf("전표 입력: %v", err)
	}
	return j
}

func TestEntityIsolationJournalsAndNumbers(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	a := e.jvIn(t, inEntity(uFinance, contract.DefaultEntityID), "기본 법인 전표")
	b := e.jvIn(t, inEntity(uFinance, abc), "계열사 전표")
	// 번호: 기본 법인은 이전 형식, 다른 법인은 법인 코드가 앞에 붙고 순번이 따로 올라간다.
	if !strings.HasPrefix(a.No, "JV-") || !strings.HasPrefix(b.No, "ABC-JV-") || !strings.HasSuffix(b.No, "-00001") {
		t.Fatalf("번호: %s %s", a.No, b.No)
	}
	// 읽기: 각 법인에서는 자기 전표만, 전체 모드에서는 둘 다.
	list := func(ctx context.Context) []string {
		page, err := e.journals.List(ctx, domain.JournalFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var nos []string
		for _, j := range page.Rows {
			nos = append(nos, j.No)
		}
		return nos
	}
	if got := list(inEntity(uFinance, contract.DefaultEntityID)); len(got) != 1 || got[0] != a.No {
		t.Fatalf("기본 법인 목록: %v", got)
	}
	if got := list(inEntity(uFinance, abc)); len(got) != 1 || got[0] != b.No {
		t.Fatalf("계열사 목록: %v", got)
	}
	if got := list(inEntity(uFinance, 0)); len(got) != 2 {
		t.Fatalf("전체 모드 목록: %v", got)
	}
	// 다른 법인의 전표는 상세·수정이 모두 없는 것처럼 거부된다(존재를 알리지 않는다).
	if _, err := e.journals.Get(inEntity(uFinance, abc), a.No); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("다른 법인 상세: %v", err)
	}
	if _, err := e.journals.RequestApproval(inEntity(uFinance, abc), a.No); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("다른 법인 전표 승인 요청: %v", err)
	}
	// 같은 법인 두 번째 전표는 순번이 이어진다.
	if b2 := e.jvIn(t, inEntity(uFinance, abc), "계열사 둘째"); !strings.HasSuffix(b2.No, "-00002") {
		t.Fatalf("순번: %s", b2.No)
	}
}

func TestEntityPermissionIsPerEntityAndGroupModeNeedsGroupScope(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	p := users[uFinance]
	p.Channel = contract.ChannelWeb
	// 기본 법인에서만 ledger:read 가 효력인 사람.
	p.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermLedgerRead: contract.NewEntitySet(contract.DefaultEntityID)}
	mk := func(a contract.ActiveEntity) context.Context {
		return contract.WithActiveEntity(contract.WithPrincipal(context.Background(), p), a)
	}
	if _, err := e.journals.List(mk(contract.ActiveEntity{ID: contract.DefaultEntityID}), domain.JournalFilter{}); err != nil {
		t.Fatalf("효력 있는 법인: %v", err)
	}
	if _, err := e.journals.List(mk(contract.ActiveEntity{ID: abc}), domain.JournalFilter{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("효력 없는 법인은 거부: %v", err)
	}
	// 전체 모드는 그룹 범위가 있어야 한다.
	if _, err := e.journals.List(mk(contract.ActiveEntity{All: true}), domain.JournalFilter{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("그룹 범위 없이 전체 모드: %v", err)
	}
	p.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermLedgerRead: {All: true}}
	if _, err := e.journals.List(mk(contract.ActiveEntity{All: true}), domain.JournalFilter{}); err != nil {
		t.Fatalf("그룹 범위 전체 모드: %v", err)
	}
}

func TestEntityMonthCloseIsPerEntity(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	// 계열사만 11월을 마감해도 기본 법인의 11월 전표는 쓸 수 있고, 계열사는 쓸 수 없다.
	if _, err := e.db.Exec(`INSERT INTO month_closes (entity_id, month, status, version, closed_by, closed_at) VALUES (?, '2026-11', 'closed', 1, ?, '2026-12-01T00:00:00Z')`, abc, uAdmin); err != nil {
		t.Fatal(err)
	}
	e.jvIn(t, inEntity(uFinance, contract.DefaultEntityID), "기본 법인 11월")
	if _, err := e.journals.Create(inEntity(uFinance, abc), domain.JournalInput{PostingDate: "2026-11-10", TradeDate: "2026-11-05", Memo: "계열사 11월", Lines: []domain.JournalLineInput{
		{AccountCode: "1210", Side: "D", Amount: 100}, {AccountCode: "1310", Side: "C", Amount: 100}}}); err == nil {
		t.Fatal("마감된 계열사의 11월에 전표가 만들어졌다")
	}
}

func TestVendorLinkEntity(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	v := e.poVendor(t, "계열 거래처", "220-81-62517") // 계열사의 사업자번호와 같다
	rows, err := e.inventory.ListVendorLinks(inEntity(uFinance, contract.DefaultEntityID))
	if err != nil {
		t.Fatal(err)
	}
	var got *struct{ sug, linked int64 }
	for _, r := range rows {
		if r.VendorID == v.ID {
			got = &struct{ sug, linked int64 }{r.SuggestedEntity, r.LinkedEntityID}
		}
	}
	if got == nil || got.sug != abc || got.linked != 0 {
		t.Fatalf("제안: %+v", got)
	}
	// 현재 법인 자신과 없는 법인은 연결할 수 없고, 계열사로는 연결된다. 직원은 연결할 수 없다.
	if _, err := e.inventory.SetVendorLink(inEntity(uFinance, contract.DefaultEntityID), v.ID, contract.DefaultEntityID); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("자기 자신: %v", err)
	}
	if _, err := e.inventory.SetVendorLink(inEntity(uFinance, contract.DefaultEntityID), v.ID, 999); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("없는 법인: %v", err)
	}
	if _, err := e.inventory.SetVendorLink(inEntity(uStaff, contract.DefaultEntityID), v.ID, abc); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	if l, err := e.inventory.SetVendorLink(inEntity(uFinance, contract.DefaultEntityID), v.ID, abc); err != nil || l.LinkedEntityID != abc {
		t.Fatalf("연결: %+v %v", l, err)
	}
	if !e.rec.Has("vendor.link", "vendor", "success") {
		t.Error("감사 vendor.link")
	}
	if _, err := e.inventory.SetVendorLink(inEntity(uFinance, contract.DefaultEntityID), v.ID, 0); err != nil {
		t.Fatalf("해제: %v", err)
	}
}
