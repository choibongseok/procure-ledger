package bootstrap_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// cmd/server 와 cmd/erp-mcp 가 쓰는 조립이 모든 서비스를 채운다.
func TestBuildWiresEveryService(t *testing.T) {
	cfg := dbtest.Config(t, config.Config{DevIDP: true, BaseURL: "http://127.0.0.1:1"})
	d, closeFn, err := bootstrap.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	checks := map[string]bool{
		"DB": d.DB != nil, "Txs": d.Txs != nil, "Audit": d.Audit != nil, "AuditReader": d.AuditReader != nil,
		"Authenticator": d.Authenticator != nil, "Masker": d.Masker != nil, "Tokens": d.Tokens != nil,
		"Users": d.Users != nil, "AuthHTTP": d.AuthHTTP != nil, "LLM": d.LLM != nil, "Documents": d.Documents != nil,
		"Purchase": d.Purchase != nil, "Approval": d.Approval != nil, "Inventory": d.Inventory != nil,
		// 개인정보 조회 기록기(B41): 원문이 나가는 모든 경로에 같은 기록기가 꽂혀 있어야 한다(운영 조립에서 nil 이면 원문 경로가 거부된다).
		"Privacy": d.Privacy != nil, "Export.Privacy": d.Export != nil && d.Export.Privacy != nil,
		"Funds.Privacy":     d.Funds != nil && d.Funds.(*service.FundService).Privacy != nil,
		"Documents.Privacy": d.Documents != nil && d.Documents.(*docverify.Service).Privacy != nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("deps.%s 가 비어 있음", name)
		}
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n == 0 {
		t.Error("dev IdP 를 켰는데 시드 사용자가 없음")
	}
}

func TestBuildDevIdPOffSeedsNothing(t *testing.T) {
	d, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Errorf("dev IdP 가 꺼져 있는데 사용자 %d 명이 시드됨", n)
	}
}

// 첨부 상한 설정이 잘못되면 기동(조립)이 거부되고, 올바른 값은 요청 크기 제한에 전달된다.
func TestBuildAttachLimits(t *testing.T) {
	for name, tweak := range map[string]func(*config.Config){
		"파일당": func(c *config.Config) { c.AttachMaxFileBytes = -1 },
		"개수":  func(c *config.Config) { c.AttachMaxFiles = -1 },
		"합계":  func(c *config.Config) { c.AttachMaxTotalBytes = -1 },
	} {
		cfg := dbtest.Config(t, config.Config{})
		tweak(&cfg)
		if _, closeFn, err := bootstrap.Build(context.Background(), cfg); err == nil {
			closeFn()
			t.Errorf("%s: 잘못된 첨부 상한인데 기동이 거부되지 않음", name)
		} else if !strings.Contains(err.Error(), "PL_ATTACH_") {
			t.Errorf("%s: 오류 문구에 설정 이름이 없음: %v", name, err)
		}
	}
	d, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{AttachMaxFileBytes: 4096, AttachMaxFiles: 2, AttachMaxTotalBytes: 8192}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if d.AttachMaxFileBytes != 4096 {
		t.Errorf("요청 크기 제한에 쓰는 값 %d", d.AttachMaxFileBytes)
	}
	d2, closeFn2, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn2()
	if d2.AttachMaxFileBytes != 10<<20 {
		t.Errorf("기본값 %d", d2.AttachMaxFileBytes)
	}
}

// B28: PL_EXPORT_MAX_ROWS 가 범위 밖이면 기동이 거부되고(설정 이름이 오류에 있다), 값은 내보내기 환경에 전달된다. 0 은 기본 10,000 이다.
func TestBuildExportMaxRows(t *testing.T) {
	if _, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{ExportMaxRows: -1})); err == nil {
		closeFn()
		t.Fatal("잘못된 내보내기 상한인데 기동이 거부되지 않음")
	} else if !strings.Contains(err.Error(), "PL_EXPORT_MAX_ROWS") {
		t.Errorf("오류 문구에 설정 이름이 없음: %v", err)
	}
	d, closeFn, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{ExportMaxRows: 500}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if d.Export == nil || d.Export.MaxRows != 500 {
		t.Errorf("내보내기 환경 %+v", d.Export)
	}
	d2, closeFn2, err := bootstrap.Build(context.Background(), dbtest.Config(t, config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn2()
	if d2.Export == nil || d2.Export.MaxRows != 10000 {
		t.Errorf("기본 상한 %+v", d2.Export)
	}
}
