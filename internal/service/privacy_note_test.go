package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// B41 ①: 거래처 목록·발주서(계좌·사업자번호 원문)와 다른 사람의 카드 내역 열람 기록.

type fakeNoter struct {
	got  []privacy.Access
	by   []contract.Principal
	fail error
}

func (f *fakeNoter) Note(_ context.Context, p contract.Principal, a privacy.Access) error {
	if f.fail != nil {
		return f.fail
	}
	if !privacy.ValidScreen(a.Screen) {
		return fmt.Errorf("허용되지 않은 화면 이름: %q", a.Screen)
	}
	f.got, f.by = append(f.got, a), append(f.by, p)
	return nil
}

func (f *fakeNoter) NoteTx(ctx context.Context, _ contract.Tx, p contract.Principal, a privacy.Access) error {
	return f.Note(ctx, p, a)
}

// withPrivacy 는 같은 DB 위에 기록기를 꽂은 서비스 묶음을 만든다.
func (e *env) withPrivacy() (*fakeNoter, *service.InventoryService, *service.POPrintService, *service.CardService) {
	n := &fakeNoter{}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }, Privacy: n}
	return n, service.NewInventoryService(b), service.NewPOPrintService(b), service.NewCardService(b)
}

func TestPrivacyNoteVendorList(t *testing.T) {
	e := newEnv(t)
	v := e.vendor(t) // 계좌 110-123-456789, 사업자번호 123-45-67890
	n, inv, _, _ := e.withPrivacy()
	// mask:bypass 가 있는 재무: 원문이 나가므로 응답 한 번에 한 번 기록한다.
	vs, err := inv.ListVendors(as(uFinance))
	if err != nil || len(vs) == 0 || vs[0].AccountNo != "110-123-456789" {
		t.Fatalf("원문이 나가야 함: %+v %v", vs, err)
	}
	if len(n.got) != 2 { // 범주별 한 건: 계좌·사업자번호(account) 와 전화·이메일(contact)
		t.Fatalf("기록 %d건", len(n.got))
	}
	if c := n.got[1]; c.Category != privacy.CatContact || c.Screen != privacy.ScreenVendorList || strings.Join(c.Fields, ",") != "phone" || len(c.Subjects) != 1 || c.Subjects[0].ID != v.ID {
		t.Fatalf("연락처 기록: %+v", c)
	}
	g := n.got[0]
	if g.Category != privacy.CatAccount || g.Screen != privacy.ScreenVendorList || len(g.Subjects) != 1 || g.Subjects[0] != (privacy.Subject{Type: privacy.SubjectVendor, ID: v.ID}) ||
		strings.Join(g.Fields, ",") != "account_no,business_no" || n.by[0].UserID != uFinance {
		t.Fatalf("기록 내용: %+v", g)
	}
	if s := fmt.Sprintf("%+v", g); strings.Contains(s, "456789") || strings.Contains(s, "123-45") {
		t.Fatalf("기록에 원문이 있다: %s", s)
	}
	// 가려진 응답(직원)은 기록 없음.
	n.got = nil
	if vs, err := inv.ListVendors(as(uStaff)); err != nil || vs[0].AccountNo == "110-123-456789" {
		t.Fatalf("가려져야 함: %+v %v", vs, err)
	}
	if len(n.got) != 0 {
		t.Fatalf("가린 응답이 기록됨: %+v", n.got)
	}
	// 기록 실패: 원문 목록을 내주지 않는다.
	n.fail = errors.New("디스크 가득 참")
	vs, err = inv.ListVendors(as(uFinance))
	if !errors.Is(err, service.ErrPrivacyNote) || vs != nil || strings.Contains(err.Error(), "디스크") {
		t.Fatalf("fail-closed: %v %v", err, vs)
	}
	if vs, err := inv.ListVendors(as(uStaff)); err != nil || len(vs) == 0 {
		t.Fatalf("가린 목록은 기록기 장애와 무관하게 나가야 함: %v", err)
	}
}

func TestPrivacyNotePOPrint(t *testing.T) {
	e := newEnv(t)
	v := e.poVendor(t, "마스킹상사", "555-55-55555")
	po := e.orderFor(t, v, "2026-12-01", 1_000, 2)
	n, _, pp, _ := e.withPrivacy()
	if p, err := pp.Print(as(uFinance), po.PRID); err != nil || p.BusinessNo != "555-55-55555" {
		t.Fatalf("원문: %+v %v", p, err)
	}
	if len(n.got) != 2 || n.got[0].Screen != privacy.ScreenPOPrint || n.got[0].Category != privacy.CatAccount ||
		len(n.got[0].Subjects) != 1 || n.got[0].Subjects[0].ID != v.ID || strings.Join(n.got[0].Fields, ",") != "business_no" ||
		n.got[1].Category != privacy.CatContact || strings.Join(n.got[1].Fields, ",") != "phone" || n.got[1].Subjects[0].ID != v.ID {
		t.Fatalf("기록: %+v", n.got)
	}
	n.got = nil
	if p, err := pp.Print(as(uStaff), po.PRID); err != nil || p.BusinessNo == "555-55-55555" || len(n.got) != 0 {
		t.Fatalf("가린 발주서: %+v %v 기록 %d", p, err, len(n.got))
	}
	n.fail = errors.New("x")
	if p, err := pp.Print(as(uFinance), po.PRID); !errors.Is(err, service.ErrPrivacyNote) || p.BusinessNo != "" {
		t.Fatalf("fail-closed: %v %+v", err, p)
	}
}

// 카드 내역: 소지자가 아닌 사람의 열람(재무 전체 목록·상세, 조직장 확인 목록)을 기록한다. 소지자 본인은 기록 없음.
func TestPrivacyNoteCardTxns(t *testing.T) {
	e := newEnv(t)
	e.card(t) // 소지자 uStaff
	e.importCards(t, cardRow("2026-11-08 12:00:00", "가맹점", 50_000, "A1", "승인"))
	txn := e.txnByApproval(t, "A1", "approve")
	e.explainOK(t, txn, "5210", true, e.receipt(t)) // 소명됨 → 조직장 확인 대상
	n, _, _, cards := e.withPrivacy()

	if _, err := cards.GetTxn(as(uStaff), txn.ID); err != nil || len(n.got) != 0 {
		t.Fatalf("본인 열람은 기록 없음: %v %d", err, len(n.got))
	}
	if _, err := cards.GetTxn(as(uFinance), txn.ID); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 {
		t.Fatalf("상세 기록 %d건", len(n.got))
	}
	g := n.got[0]
	if g.Category != privacy.CatCard || g.Screen != privacy.ScreenCardTxn || len(g.Subjects) != 1 || g.Subjects[0] != (privacy.Subject{Type: privacy.SubjectUser, ID: uStaff}) ||
		strings.Join(g.Fields, ",") != "card_txn" || n.by[0].UserID != uFinance {
		t.Fatalf("기록 내용: %+v", g)
	}
	// 전체 목록: 응답 한 번에 한 번, 소지자 한 명.
	n.got = nil
	all, _, err := cards.All(as(uFinance), domain.CardFilter{Limit: 100})
	if err != nil || len(all) == 0 || len(n.got) != 1 || len(n.got[0].Subjects) != 1 {
		t.Fatalf("전체 목록: %v 기록 %+v", err, n.got)
	}
	// 조직장 확인 목록.
	n.got = nil
	if list, err := cards.ToConfirm(as(uLead)); err != nil || len(list) != 1 || len(n.got) != 1 || n.got[0].Subjects[0].ID != uStaff {
		t.Fatalf("확인 목록: %v %v 기록 %+v", err, list, n.got)
	}
	// 내 내역은 기록 없음.
	n.got = nil
	if _, err := cards.Mine(as(uStaff), domain.CardFilter{}); err != nil || len(n.got) != 0 {
		t.Fatalf("내 내역: %v 기록 %d", err, len(n.got))
	}
	// 기록 실패: 내역을 내주지 않는다.
	n.fail = errors.New("x")
	if got, err := cards.GetTxn(as(uFinance), txn.ID); !errors.Is(err, service.ErrPrivacyNote) || got.ID != 0 {
		t.Fatalf("상세 fail-closed: %v %+v", err, got)
	}
	if got, _, err := cards.All(as(uFinance), domain.CardFilter{Limit: 100}); !errors.Is(err, service.ErrPrivacyNote) || got != nil {
		t.Fatalf("목록 fail-closed: %v", err)
	}
	if got, err := cards.ToConfirm(as(uLead)); !errors.Is(err, service.ErrPrivacyNote) || got != nil {
		t.Fatalf("확인 목록 fail-closed: %v", err)
	}
	if _, err := cards.GetTxn(as(uStaff), txn.ID); err != nil {
		t.Fatalf("본인 열람은 기록기 장애와 무관: %v", err)
	}
}
