package privacy_test

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B42: API 키 요청의 화면 이름은 "API <경로>" 형식이고 api 경로로 기록된다.
func TestAPIScreenAndChannel(t *testing.T) {
	for _, ok := range []string{"API /api/v1/vendors", "API GET /api/v1/payments/12"} {
		if !privacy.ValidScreen(ok) {
			t.Errorf("%q 는 허용되어야 한다", ok)
		}
	}
	for _, bad := range []string{"API", "api /x", "API " + strings.Repeat("a", 80), ""} {
		if privacy.ValidScreen(bad) {
			t.Errorf("%q 는 거부되어야 한다", bad)
		}
	}
	e := newEnv(t)
	p := contract.Principal{UserID: e.viewer, Role: contract.RoleAdmin, Channel: contract.ChannelAPI, Auth: contract.AuthToken}
	a := privacy.Access{Category: privacy.CatContact, Screen: "API /api/v1/vendors", Fields: []string{privacy.FieldEmail}, Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: e.subj1}}}
	if err := e.rec.Note(e.ctx, p, a); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM privacy_access_log WHERE channel = 'api' AND screen = 'API /api/v1/vendors'`) != 1 {
		t.Fatal("api 경로 기록이 없다")
	}
	if res, err := e.rec.Verify(e.ctx); err != nil || !res.OK {
		t.Fatalf("체인 검증 %+v %v", res, err)
	}
}
