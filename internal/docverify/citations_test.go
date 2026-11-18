package docverify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

const supplierMixedLine = "공급자: 한빛상사 010-9999-8888 a@b.example"

// 공급자 줄에 전화번호·이메일이 섞여도 정상 문서는 판단 보류가 아니라 일치다(마스킹 전 원문 기준으로 인용을 대조한다).
// 응답은 열람자 역할의 마스킹을 따르고, 강조 위치는 가려진 원문 위에서 어긋나지 않는다.
func TestMaskedValuesOnSameLineStillGrounded(t *testing.T) {
	doc := strings.Replace(goodDoc, "공급자: 한빛상사", supplierMixedLine, 1)
	e := newEnvMasker(t, rbac.Masker{})
	v := e.verify(t, finance, boltSpec(), doc)
	if v.Outcome != domain.OutcomeMatch {
		t.Fatalf("정상 문서는 일치여야 한다: %s %+v", v.Outcome, v.Items)
	}
	// 모델에는 마스킹된 텍스트만 나갔다.
	for _, r := range e.spy.sent {
		for _, m := range r.Messages {
			if strings.Contains(m.Content, "010-9999-8888") || strings.Contains(m.Content, "a@b.example") {
				t.Fatalf("원문이 모델로 나갔다: %q", m.Content)
			}
		}
	}
	fin, err := e.svc.GetVerdict(ctxAs(finance), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := fin.Extraction.Citations["supplier"]; sliceRunes(fin.SourceText, c) != c.Quote || c.Quote == "" {
		t.Fatalf("재무(원문)의 공급자 인용은 원문 구간이어야 한다: %+v", c)
	}
	for _, role := range []contract.Role{contract.RoleStaff, contract.RoleAuditor} {
		got, err := e.svc.GetVerdict(ctxAs(kit.Principal(role)), v.ID)
		if err != nil {
			t.Logf("%s 열람 불가: %v", role, err)
			continue
		}
		blob := got.SourceText
		for _, c := range got.Extraction.Citations {
			blob += "|" + c.Quote
			if sliceRunes(got.SourceText, c) != c.Quote {
				t.Errorf("%s: 가려진 원문 위에서 위치 어긋남: %+v", role, c)
			}
		}
		for _, raw := range []string{"010-9999-8888", "a@b.example", "123-45-67890"} {
			if strings.Contains(blob, raw) {
				t.Errorf("%s: 응답에 %s 원문이 나감", role, raw)
			}
		}
		if len(got.Extraction.Citations) < 10 {
			t.Errorf("%s: 인용 유지: %d", role, len(got.Extraction.Citations))
		}
	}
}

// 마스킹이 섞인 줄에서도 위조 인용은 여전히 보류다.
func TestForgedCitationStillHeldWithMaskedLine(t *testing.T) {
	doc := strings.Replace(goodDoc, "공급자: 한빛상사", supplierMixedLine, 1) + "\n비고: 인용위조"
	e := newEnvMasker(t, rbac.Masker{})
	v := e.verify(t, finance, boltSpec(), doc)
	if v.Outcome != domain.OutcomeHold {
		t.Fatalf("보류여야 함: %s %+v", v.Outcome, v.Items)
	}
}

func sliceRunes(s string, c domain.Citation) string {
	rs := []rune(s)
	if c.Start < 0 || c.End > len(rs) || c.Start > c.End {
		return "<범위 밖>"
	}
	return string(rs[c.Start:c.End])
}

// 모든 추출 필드에 원문 인용이 있고, 인용 구간이 원문에서 그대로 잘라 낸 구절이다.
func TestCitationsPointToRealSourceText(t *testing.T) {
	e := newEnv(t)
	v := e.verify(t, staff, boltSpec(), goodDoc)
	got, err := e.svc.GetVerdict(ctxAs(finance), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceText != goodDoc {
		t.Fatalf("재무(mask:bypass)는 원문 그대로: %q", got.SourceText)
	}
	want := []string{"supplier", "business_no", "line[0].name", "line[0].quantity", "line[0].unit_price", "line[0].amount",
		"line[1].name", "line[1].quantity", "line[1].unit_price", "line[1].amount", "total_amount"}
	for _, k := range want {
		c, ok := got.Extraction.Citations[k]
		if !ok || c.Quote == "" || sliceRunes(got.SourceText, c) != c.Quote {
			t.Errorf("%s 인용이 원문과 맞지 않음: %+v", k, c)
		}
	}
	if c := got.Extraction.Citations["business_no"]; c.Quote != "123-45-67890" {
		t.Fatalf("사업자번호 인용: %+v", c)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM document_extractions WHERE extraction_json LIKE '%"citations"%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("저장된 추출에 인용: %d %v", n, err)
	}
}

// 마스킹 대상이 든 원문은 가려서 내보내되, 인용 위치는 가려진 원문 위에서 어긋나지 않는다.
func TestCitationsStayAlignedOnMaskedSource(t *testing.T) {
	e := newEnv(t)
	v := e.verify(t, staff, boltSpec(), goodDoc)
	got, err := e.svc.GetVerdict(ctxAs(staff), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.SourceText, "123-45-67890") || !strings.Contains(got.SourceText, "[MASKED]") {
		t.Fatalf("staff 원문은 가려야 한다: %q", got.SourceText)
	}
	if len(got.Extraction.Citations) < 10 {
		t.Fatalf("가려도 인용은 유지: %d", len(got.Extraction.Citations))
	}
	for k, c := range got.Extraction.Citations {
		if sliceRunes(got.SourceText, c) != c.Quote {
			t.Errorf("%s: 가려진 원문 위에서 위치가 어긋남: %+v", k, c)
		}
		if strings.Contains(c.Quote, "123-45-67890") {
			t.Errorf("%s: 인용에 마스킹 대상 원문이 나감", k)
		}
	}
	if strings.Contains(v.SourceText, "123-45-67890") {
		t.Fatalf("판정 응답의 원문도 가려야 한다: %q", v.SourceText)
	}
}

// 타 부서 직원은 원문·인용을 볼 수 없다(판정 자체가 없는 것처럼 답한다).
func TestSourceTextHiddenAcrossDepartments(t *testing.T) {
	e := newEnv(t)
	v := e.verify(t, staff, boltSpec(), goodDoc)
	other := kit.Principal(contract.RoleStaff)
	other.UserID, other.DepartmentID = kit.OtherStaffID, kit.OtherDeptID
	got, err := e.svc.GetVerdict(ctxAs(other), v.ID)
	if err == nil || got.SourceText != "" || len(got.Extraction.Citations) != 0 {
		t.Fatalf("타 부서에는 원문·인용이 없다: %+v %v", got, err)
	}
	list, _ := e.svc.ListVerdicts(ctxAs(staff), 10)
	for _, lv := range list {
		if lv.SourceText != "" {
			t.Fatal("목록에는 원문을 싣지 않는다")
		}
	}
}

// 인용 위조: 원문에 없는 구절, 같은 숫자가 있는 다른 줄의 구절은 근거 없음이고 판정은 일치가 될 수 없다.
func TestForgedCitationsCannotMatch(t *testing.T) {
	moved := strings.Replace(goodDoc, "수량 20 | 단가 100 | 금액 2000", "수량 10 | 단가 100 | 금액 1000", 1)
	moved = strings.Replace(moved, "합계: 7000", "합계: 6000", 1) + "\n비고: 인용이동"
	cases := map[string]struct {
		doc  string
		qty2 int64
	}{
		"원문에 없는 인용":   {goodDoc + "\n비고: 인용위조", 20},
		"다른 줄의 같은 숫자": {moved, 10},
	}
	for name, c := range cases {
		e := newEnv(t)
		spec := boltSpec()
		spec.Lines[1].Qty = c.qty2
		v := e.verify(t, staff, spec, c.doc)
		if v.Outcome != domain.OutcomeHold {
			t.Errorf("%s: 보류여야 함: %s %+v", name, v.Outcome, v.Items)
			continue
		}
		it, ok := find(v, "extraction")
		if !ok || !strings.Contains(it.Note, "근거를 확인하지 못한") {
			t.Errorf("%s: 근거 없음 안내: %+v", name, v.Items)
		}
	}
}

// 이미지 경로는 원문 텍스트가 없어 인용도 원문도 없고, 기존 교차 확인 안내가 유지된다.
func TestImagePathHasNoCitations(t *testing.T) {
	e := newEnv(t)
	setImageSetting(t, e, true)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	in := imageInput(po, rc)
	in.Image = []byte(goodDoc)
	v, err := e.svc.Verify(ctxAs(finance), in)
	if err != nil {
		t.Fatal(err)
	}
	if v.SourceText != "" || len(v.Extraction.Citations) != 0 {
		t.Fatalf("이미지는 인용·원문 없음: %+v", v)
	}
	if _, ok := find(v, "grounding"); !ok {
		t.Fatal("원문 교차 확인 안내 유지")
	}
}
