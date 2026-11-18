package taxaspadmin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/acctsecret"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/taxaspadmin"
)

// B43 후속: 모의 제공자도 내부 주소를 저장·활성할 수 없다(실제 어댑터와 같은 주소 규칙).
func TestMockProviderRejectsInternalEndpoints(t *testing.T) {
	e := newEnv(t, false)
	for _, bad := range []string{
		"https://127.0.0.1/x", "https://10.0.0.5/x", "https://169.254.169.254/latest", "https://localhost/x", "http://api.asp.example.com/x", "https://user:pw@api.asp.example.com/x",
	} {
		_, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "mock", Endpoint: bad})
		if !errors.Is(err, contract.ErrValidation) {
			t.Errorf("%s: 거부돼야 한다: %v", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("오류 문장에 주소가 들어 있다: %v", err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM tax_asp_settings`); n != 0 {
		t.Fatalf("거부된 저장이 행을 만들었다: %d", n)
	}
	// 공개 https 주소는 모의도 저장된다.
	if _, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "mock", Endpoint: "https://api.asp.example.com/mock"}); err != nil {
		t.Fatalf("공개 주소: %v", err)
	}
}

// 규칙이 생기기 전에 저장된 내부 주소는 켜기에서 막는다.
func TestActivateRefusesPreviouslyStoredInternalEndpoint(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.svc.Save(e.ctx(), taxaspadmin.SaveInput{Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	c, err := acctsecret.New(e.key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.SealFor(taxaspadmin.SealPurpose, `{"endpoint":"https://127.0.0.1/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE tax_asp_settings SET secret_sealed = ?`, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetActive(e.ctx(), true); !errors.Is(err, contract.ErrInvalidState) {
		t.Fatalf("내부 주소가 저장된 모의는 켜지면 안 된다: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM tax_asp_settings WHERE active = 1`); n != 0 {
		t.Fatalf("활성됨: %d", n)
	}
}
