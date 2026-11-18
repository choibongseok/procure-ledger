package docverify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// B41: 문서 검증 응답에 사업자번호 원문이 실리면 열람을 기록한다(웹, mask:bypass). MCP 채널은 권한자에게도 가린다(D6).

type noteSpy struct {
	got  []privacy.Access
	fail error
}

func (n *noteSpy) Note(_ context.Context, _ contract.Principal, a privacy.Access) error {
	if n.fail != nil {
		return n.fail
	}
	if !privacy.ValidScreen(a.Screen) {
		return fmt.Errorf("허용되지 않은 화면 이름: %q", a.Screen)
	}
	n.got = append(n.got, a)
	return nil
}

func (n *noteSpy) NoteTx(ctx context.Context, _ contract.Tx, p contract.Principal, a privacy.Access) error {
	return n.Note(ctx, p, a)
}

func TestVerdictBusinessNoRawIsNotedAndMCPIsMasked(t *testing.T) {
	e := newEnvMasker(t, rbac.Masker{})
	v := e.verify(t, staff, boltSpec(), goodDoc)
	spy := &noteSpy{}
	e.svc.Privacy = spy
	var vendorID int64
	if err := e.db.QueryRow(`SELECT vendor_id FROM purchase_orders WHERE id = ?`, v.POID).Scan(&vendorID); err != nil || vendorID == 0 {
		t.Fatalf("발주 거래처: %d %v", vendorID, err)
	}
	// 가려진 응답(직원)은 기록 없음.
	if _, err := e.svc.GetVerdict(ctxAs(staff), v.ID); err != nil || len(spy.got) != 0 {
		t.Fatalf("직원: %v 기록 %d", err, len(spy.got))
	}
	// 재무(웹, mask:bypass): 원문이 나가므로 기록한다. 정보주체는 발주의 거래처다.
	got, err := e.svc.GetVerdict(ctxAs(finance), v.ID)
	if err != nil || got.Extraction.BusinessNo != "123-45-67890" {
		t.Fatalf("원문: %+v %v", got.Extraction, err)
	}
	if len(spy.got) != 1 {
		t.Fatalf("기록 %d건", len(spy.got))
	}
	a := spy.got[0]
	if a.Category != privacy.CatAccount || a.Screen != privacy.ScreenVerifyDoc || len(a.Subjects) != 1 || a.Subjects[0] != (privacy.Subject{Type: privacy.SubjectVendor, ID: vendorID}) ||
		strings.Join(a.Fields, ",") != "business_no" {
		t.Fatalf("기록 내용: %+v", a)
	}
	if strings.Contains(fmt.Sprintf("%+v", a), "123-45-67890") {
		t.Fatal("기록에 원문이 있다")
	}
	// 목록도 응답 한 번에 한 번이다.
	spy.got = nil
	if list, err := e.svc.ListVerdicts(ctxAs(finance), 10); err != nil || len(list) == 0 || len(spy.got) != 1 {
		t.Fatalf("목록: %v %d 기록 %d", err, len(list), len(spy.got))
	}
	// MCP 채널은 mask:bypass 가 있어도 가린다(D6). 기록도 필요 없다.
	spy.got = nil
	mcp := finance
	mcp.Channel = contract.ChannelMCP
	m, err := e.svc.GetVerdict(contract.WithPrincipal(context.Background(), mcp), v.ID)
	if err != nil || strings.Contains(m.Extraction.BusinessNo, "123-45-67890") || strings.Contains(m.SourceText, "123-45-67890") {
		t.Fatalf("MCP 는 가려야 함: %+v %v", m.Extraction, err)
	}
	for _, it := range m.Items {
		if strings.Contains(it.Expected, "123-45-67890") || strings.Contains(it.Actual, "123-45-67890") {
			t.Fatalf("MCP 근거 항목에 원문: %+v", it)
		}
	}
	if len(spy.got) != 0 {
		t.Fatalf("MCP 응답이 기록됨: %+v", spy.got)
	}
	// 기록 실패: 원문 판정을 내주지 않는다. 가려진 응답은 영향이 없다.
	spy.fail = errors.New("디스크 가득 참")
	if got, err := e.svc.GetVerdict(ctxAs(finance), v.ID); !errors.Is(err, docverify.ErrPrivacyNote) || got.ID != 0 || strings.Contains(err.Error(), "디스크") {
		t.Fatalf("fail-closed: %v %+v", err, got)
	}
	if _, err := e.svc.GetVerdict(ctxAs(staff), v.ID); err != nil {
		t.Fatalf("가린 응답은 기록기 장애와 무관: %v", err)
	}
}
