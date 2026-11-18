package service_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// B34 단계 1: 설정이 없으면 현행과 같고(회사명은 조직도 이름), 저장하면 출력이 그 값을 쓴다. 변경은 admin 만, 이전·새 값이 감사에 남는다.
func TestCompanyInfoDefaultsAndPrintFollowsSettings(t *testing.T) {
	e := newEnv(t)
	ci, err := e.company.Info(as(uStaff))
	if err != nil || ci.Configured || ci.Name != "" {
		t.Fatalf("설정 전: %+v %v", ci, err)
	}
	v := e.poVendor(t, "발주상사", "555-55-55555")
	po := e.orderFor(t, v, "", 12_345, 1)
	p, err := e.poPrint.Print(as(uStaff), po.PRID)
	if err != nil || p.Company != "PL 주식회사" || p.CompanyBizNo != "" || p.HasLogo || p.HasSeal || p.VAT != 1_234 {
		t.Fatalf("설정 전 발주서는 현행과 같아야 함: %+v %v", p, err)
	}
	in := domain.CompanyInput{Name: "에이엑스 주식회사", BusinessNo: "123-45-67890", Representative: "홍길동", Address: "서울", Phone: "02-123-4567"}
	for _, who := range []int64{uStaff, uLead, uFinance, uAuditor} {
		if _, err := e.company.Update(as(who), in, 0); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 회사 정보 변경: %v", who, err)
		}
	}
	got, err := e.company.Update(as(uAdmin), in, 0)
	if err != nil || !got.Configured || got.Version != 1 || got.Name != in.Name {
		t.Fatalf("저장: %+v %v", got, err)
	}
	p, err = e.poPrint.Print(as(uStaff), po.PRID)
	if err != nil || p.Company != in.Name || p.CompanyBizNo != in.BusinessNo || p.CompanyRep != in.Representative || p.CompanyAddr != in.Address || p.CompanyPhone != in.Phone {
		t.Fatalf("저장한 회사 정보가 발주서에 반영되어야 함: %+v %v", p, err)
	}
	if !e.rec.Has("company.update", audit.TargetSetting, audit.ResultSuccess) || !e.rec.Has("company.update", audit.TargetSetting, audit.ResultDenied) {
		t.Error("변경 성공·거부 감사가 남아야 함")
	}
	// 낙관적 잠금과 검증.
	if _, err := e.company.Update(as(uAdmin), in, 0); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("낡은 버전으로 저장: %v", err)
	}
	if _, err := e.company.Update(as(uAdmin), domain.CompanyInput{Name: "  "}, got.Version); !isErr(err, contract.ErrValidation) {
		t.Errorf("회사명 비움: %v", err)
	}
	bad := in
	bad.BusinessNo = "1234567890"
	if _, err := e.company.Update(as(uAdmin), bad, got.Version); !isErr(err, contract.ErrValidation) {
		t.Errorf("사업자번호 형식: %v", err)
	}
	bad = in
	bad.Name = strings.Repeat("가", 101)
	if _, err := e.company.Update(as(uAdmin), bad, got.Version); !isErr(err, contract.ErrValidation) {
		t.Errorf("회사명 길이: %v", err)
	}
	if got, err := e.company.Update(as(uAdmin), in, got.Version); err != nil || got.Version != 2 {
		t.Errorf("정상 버전 갱신: %+v %v", got, err)
	}
}

func TestCompanyAssetsValidationHistoryAndPermissions(t *testing.T) {
	e := newEnv(t)
	good := testPNG(t, 40, 40)
	for _, who := range []int64{uStaff, uFinance, uAuditor} {
		if _, err := e.company.SetAsset(as(who), domain.CompanyAssetSeal, good); !isErr(err, contract.ErrForbidden) {
			t.Errorf("%d 직인 교체: %v", who, err)
		}
	}
	for name, data := range map[string][]byte{
		"svg":    []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"gif":    []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"text":   []byte("not an image"),
		"big":    append(append([]byte(nil), good...), make([]byte, 600*1024)...),
		"wide":   testPNG(t, 2001, 10),
		"broken": good[:20],
	} {
		if _, err := e.company.SetAsset(as(uAdmin), domain.CompanyAssetLogo, data); !isErr(err, contract.ErrValidation) {
			t.Errorf("%s 는 거부되어야 함: %v", name, err)
		}
	}
	if _, err := e.company.SetAsset(as(uAdmin), "banner", good); !isErr(err, contract.ErrValidation) {
		t.Errorf("알 수 없는 종류: %v", err)
	}
	if _, err := e.company.Asset(as(uStaff), domain.CompanyAssetSeal); !isErr(err, contract.ErrNotFound) {
		t.Fatalf("이미지가 없으면 404: %v", err)
	}
	ci, err := e.company.SetAsset(as(uAdmin), domain.CompanyAssetSeal, good)
	if err != nil || !ci.HasSeal || ci.HasLogo {
		t.Fatalf("직인 저장: %+v %v", ci, err)
	}
	second := testPNG(t, 50, 30)
	if _, err := e.company.SetAsset(as(uAdmin), domain.CompanyAssetSeal, second); err != nil {
		t.Fatal(err)
	}
	a, err := e.company.Asset(as(uStaff), domain.CompanyAssetSeal) // 로그인한 누구나 받는다
	if err != nil || a.ContentType != "image/png" || a.Width != 50 || !bytes.Equal(a.Data, second) {
		t.Fatalf("현재 직인: %+v %v", a, err)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM company_assets WHERE kind='seal'`); n != 2 {
		t.Errorf("이전 이미지는 이력으로 남아야 함: %d", n)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM company_assets WHERE kind='seal' AND current=1`); n != 1 {
		t.Errorf("현재는 하나: %d", n)
	}
	if _, err := e.db.Exec(`DELETE FROM company_assets`); err == nil {
		t.Error("이미지 삭제는 막혀야 함")
	}
	if _, err := e.db.Exec(`UPDATE company_assets SET content = ? WHERE current = 1`, []byte{0}); err == nil {
		t.Error("이미지 내용 수정은 막혀야 함")
	}
	if !e.rec.Has("company.seal_set", audit.TargetSetting, audit.ResultSuccess) {
		t.Error("직인 교체 감사가 없음")
	}
}
