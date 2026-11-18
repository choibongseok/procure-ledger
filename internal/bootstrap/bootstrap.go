// Package bootstrap 은 서비스 조립 한 벌이다.
//
// 설정 → DB 열기·마이그레이션 → platform → erp → ai 순서로 deps.Deps 를 채운다.
// cmd/server 와 cmd/erp-mcp 가 같은 Build 를 쓴다(조립이 두 벌이 되지 않게).
// 영역별 조립 파일: wire_platform.go(platform), wire_erp.go(erp), wire_ai.go(ai).
package bootstrap

import (
	"context"
	"fmt"
	"log"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/rehearsal"
	"github.com/choibongseok/procure-ledger/migrations"
)

// Build 는 DB 를 열고 마이그레이션을 적용한 뒤 모든 서비스를 조립한다.
// 반환하는 close 는 DB 를 닫는다. 실패하면 이미 연 DB 는 닫고 돌려준다.
func Build(ctx context.Context, cfg config.Config) (deps.Deps, func(), error) {
	target, err := dbutil.ParseTarget(cfg.DBPath, cfg.DBURL)
	if err != nil {
		return deps.Deps{}, func() {}, err
	}
	if cfg.Rehearsal { // 운영 DB 를 가리키면 리허설 모드는 기동 자체를 거부한다(D15). DB 를 열기 전에 막는다.
		if err := rehearsal.Allowed(target.Kind == dbutil.KindPostgres, cfg.DBPath); err != nil {
			return deps.Deps{}, func() {}, err
		}
		log.Printf("bootstrap: 복구 리허설 모드(PL_REHEARSAL=1): 주기 작업·메일/웹훅 발송·감사 앵커 기록을 하지 않습니다")
	}
	if target.Kind == dbutil.KindPostgres {
		if cfg.DBMaxConns < 0 {
			return deps.Deps{}, func() {}, fmt.Errorf("PL_DB_MAX_CONNS 는 2 이상 100 이하의 정수여야 합니다")
		}
		if target.IgnoredPath(cfg.DBPath, dbutil.DefaultSQLitePath) {
			log.Printf("bootstrap: PL_DB_URL 이 설정되어 PL_DB_PATH 는 무시됩니다")
		}
	}
	db, dialect, err := dbutil.OpenTarget(ctx, target, dbutil.Options{PG: dbutil.PGOptions{MaxConns: cfg.DBMaxConns, AllowInsecure: cfg.DBAllowInsecure}})
	if err != nil {
		return deps.Deps{}, func() {}, err
	}
	if err := migrations.ApplyDialect(ctx, db, dialect); err != nil {
		_ = db.Close()
		return deps.Deps{}, func() {}, err
	}
	d := deps.Deps{DB: db, Dialect: dialect}
	if err := Wire(cfg, &d); err != nil {
		_ = db.Close()
		return deps.Deps{}, func() {}, err
	}
	return d, func() { _ = db.Close() }, nil
}

// Wire 는 DB 가 채워진 d 에 서비스를 조립한다(플랫폼 → 업무 → AI. 뒤 단계는 앞 단계 결과를 쓴다).
func Wire(cfg config.Config, d *deps.Deps) error {
	for _, w := range []struct {
		name string
		fn   func(config.Config, *deps.Deps) error
	}{{"platform", WirePlatform}, {"erp", WireERP}, {"ai", WireAI}} {
		if err := w.fn(cfg, d); err != nil {
			return fmt.Errorf("조립(%s): %w", w.name, err)
		}
	}
	return nil
}
