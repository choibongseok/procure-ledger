package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/rehearsal"
)

// B38 D15: 리허설 모드는 운영 경로·PG 에서 DB 를 열기도 전에 기동을 거부한다(AC-1834).
func TestRehearsalRefusedOnProductionPaths(t *testing.T) {
	prod := filepath.Join("/nonexistent-pl-prod", "data", "erp.db")
	_, closeFn, err := Build(context.Background(), config.Config{DBPath: prod, Rehearsal: true})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "PL_REHEARSAL") {
		t.Fatalf("운영 경로: %v", err)
	}
	if _, serr := os.Stat(filepath.Dir(prod)); serr == nil {
		t.Error("거부했는데 디렉터리가 만들어짐")
	}
	// PG: 접속하기 전에 거부하고 오류에 접속 문자열(비밀번호)을 싣지 않는다.
	_, closeFn, err = Build(context.Background(), config.Config{DBURL: "postgres://user:s3cretpw@dbhost/erp", Rehearsal: true})
	closeFn()
	if err == nil || strings.Contains(err.Error(), "s3cretpw") || !strings.Contains(err.Error(), "PL_REHEARSAL") {
		t.Fatalf("PG: %v", err)
	}
	// 같은 경로에서 리허설 모드가 꺼져 있으면 이 검사는 관여하지 않는다(다른 이유로 실패할 수는 있다).
	if err := rehearsal.Allowed(false, prod); err == nil {
		t.Error("판정 함수가 운영 경로를 허용함")
	}
}

// 임시 위치(마커 디렉터리)에서는 켜지고, 앵커 키·SMTP 설정이 환경에 있어도 앵커와 메일 발송기가 만들어지지 않는다.
func TestRehearsalDisablesAnchorAndMail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, rehearsal.Marker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(dir, "anchor.log")
	cfg := config.Config{DBPath: filepath.Join(dir, "db.sqlite"), BaseURL: "http://localhost:8080", Rehearsal: true,
		AuditAnchorKey: "rehearsal-anchor-key-0123456789", AuditAnchorPath: anchorPath}
	cfg.Auth.SMTPHost, cfg.Auth.SMTPPort, cfg.Auth.SMTPFrom = "smtp.prod.example", "587", "erp@prod.example"
	d, closeFn, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if d.AuditAnchor != nil {
		t.Error("리허설 모드인데 감사 앵커가 켜짐")
	}
	if _, err := os.Stat(anchorPath); err == nil {
		t.Error("앵커 파일이 만들어짐")
	}
	if d.Notify == nil || d.Notify.MailConfigured() {
		t.Error("리허설 모드인데 메일 발송기가 만들어짐")
	}
	// 대조: 같은 설정에서 리허설이 꺼져 있으면 앵커와 메일이 켜진다(위 결과가 모드 때문임을 보인다).
	dir2 := t.TempDir()
	cfg2 := cfg
	cfg2.Rehearsal = false
	cfg2.DBPath = filepath.Join(dir2, "db.sqlite")
	cfg2.AuditAnchorPath = filepath.Join(dir2, "anchor.log")
	d2, close2, err := Build(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer close2()
	if d2.AuditAnchor == nil || !d2.Notify.MailConfigured() {
		t.Errorf("대조 구성에서 앵커·메일이 켜져야 함: anchor=%v mail=%v", d2.AuditAnchor != nil, d2.Notify.MailConfigured())
	}
}
