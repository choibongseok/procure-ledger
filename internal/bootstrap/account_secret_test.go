package bootstrap_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 계좌번호 암호화 키(B32 D1 후속): 형식이 틀리면 기동을 거부하고, 키가 있으면 기동 때 평문 계좌번호를 한 번 봉하며, 키가 없으면 기동은 하되 계좌번호 저장은 거부된다.
func TestAccountSecretKeyAtStartup(t *testing.T) {
	for name, key := range map[string]string{"base64 아님": "***", "길이": base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{AccountSecretKey: key})); err == nil {
			closeFn()
			t.Errorf("%s: 잘못된 키인데 기동이 거부되지 않음", name)
		} else if !strings.Contains(err.Error(), "PL_ACCOUNT_SECRET_KEY") {
			t.Errorf("%s: 오류 문구에 설정 이름이 없음: %v", name, err)
		}
	}
	// 키 없이 기동한 DB 에 평문 계좌번호를 심고, 키를 넣고 다시 기동하면 봉해진다(멱등).
	same := dbtest.Shared(t)
	d, closeFn, err := bootstrap.Build(context.Background(), same(config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	if repository.AccountKeyReady() {
		t.Fatal("키가 없는데 저장 가능 상태")
	}
	if _, err := d.DB.Exec(`INSERT INTO vendors (name, business_no, account_no, phone, email) VALUES ('평문상사','111-11-11111','110-123-456789','','')`); err != nil {
		t.Fatal(err)
	}
	closeFn()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	for i := 0; i < 2; i++ {
		d, closeFn, err = bootstrap.Build(context.Background(), same(config.Config{AccountSecretKey: key}))
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := d.DB.QueryRow(`SELECT account_no FROM vendors WHERE name = '평문상사'`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, "456789") {
			t.Fatalf("%d번째 기동: 봉해지지 않음 %q", i+1, stored)
		}
		if repository.OpenAccount(stored) != "110-123-456789" {
			t.Fatal("봉한 값이 풀리지 않음")
		}
		closeFn()
	}
	// 시험 뒤 다른 시험에 영향이 없게 키를 비운다.
	repository.SetAccountCipher(nil)
}
