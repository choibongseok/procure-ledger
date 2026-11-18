// 명령: server — 웹 API + 임베드 프론트를 서빙하는 단일 바이너리. (조립 지점은 app.go 주석).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/choibongseok/procure-ledger/internal/admincmd"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/demo"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/setup"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	// 호환 별칭: `server access-recompute`·`server auth-unlock`·`server db-migrate` 는 erp-admin 과 같은 구현을 부른다(B38 D12, B37 11절).
	if len(os.Args) > 1 && (os.Args[1] == "access-recompute" || os.Args[1] == "auth-unlock" || os.Args[1] == "db-migrate") {
		os.Exit(admincmd.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}
	if err := run(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func run() error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, closeDeps, err := bootstrap.Build(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDeps()
	// 한 데이터베이스에 서버는 하나만(B37 7.4). erp-mcp·erp-admin 은 잡지 않는다. 못 잡으면 서비스를 시작하지 않고 비 0 으로 끝난다.
	releaseInstance, err := dbutil.AcquireInstanceLock(ctx, d.DB, d.Dialect)
	if err != nil {
		return err
	}
	defer releaseInstance()
	// 개인정보 조회 기록의 하루 요약·보관 기간 파기(B41). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if runner, ok := d.Privacy.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		pctx, pcancel := context.WithCancel(ctx)
		pdone := make(chan struct{})
		go func() { runner.Run(pctx); close(pdone) }()
		defer func() { pcancel(); <-pdone }()
	}
	// 권한 분리 예외 만료 처리(B41). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if runner, ok := d.SoD.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		xctx, xcancel := context.WithCancel(ctx)
		xdone := make(chan struct{})
		go func() { runner.Run(xctx); close(xdone) }()
		defer func() { xcancel(); <-xdone }()
	}
	// 월별 점검 보고서 알림(B41): 매월 첫 영업일 이후 한 번. 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if d.PrivReport != nil && !cfg.Rehearsal {
		rctx, rcancel := context.WithCancel(ctx)
		rdone := make(chan struct{})
		go func() { d.PrivReport.Run(rctx); close(rdone) }()
		defer func() { rcancel(); <-rdone }()
	}
	// 연동 API 읽기 호출 요약 감사·옛 분 창·변경 피드 보존 정리(B42). 종료 때는 남은 요약을 감사에 남긴 뒤 DB 를 닫는다.
	if d.APITokens != nil && !cfg.Rehearsal {
		actx, acancel := context.WithCancel(ctx)
		adone := make(chan struct{})
		go func() { d.APITokens.Run(actx); close(adone) }()
		defer func() { acancel(); <-adone }()
	}
	// 변경 웹훅 발송 작업(B42 2단계): 2초마다 변경 피드를 읽어 발송한다. 복구 리허설 모드에서는 돌지 않는다. 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if d.Webhooks != nil && !cfg.Rehearsal {
		wctx, wcancel := context.WithCancel(ctx)
		wdone := make(chan struct{})
		go func() { d.Webhooks.Run(wctx); close(wdone) }()
		defer func() { wcancel(); <-wdone }()
	}
	// 사내 지식 색인 대기열 처리(B47): 결재 완료 문서를 읽어 조각으로 색인한다. 복구 리허설 모드에서는 돌지 않는다.
	if d.KnowledgeWorker != nil && !cfg.Rehearsal {
		kctx, kcancel := context.WithCancel(ctx)
		kdone := make(chan struct{})
		go func() { d.KnowledgeWorker.Run(kctx, 5*time.Second); close(kdone) }()
		defer func() { kcancel(); <-kdone }()
	}
	// 전자세금계산서 전송·상태 조회 작업(B43): 발행 대기 건을 대행사에 보내고 수신 결과를 조회한다. 복구 리허설 모드에서는 돌지 않는다(복구본이 운영 대행사에 이중 발행하지 않게).
	if d.TaxWorker != nil && !cfg.Rehearsal {
		tctx, tcancel := context.WithCancel(ctx)
		tdone := make(chan struct{})
		go func() { d.TaxWorker.Run(tctx); close(tdone) }()
		defer func() { tcancel(); <-tdone }()
	}
	// 수신 세금계산서 동기화(B43 2단계): 하루 한 번 대행사의 수신 목록을 읽는다(겹침 3일). 리허설에서는 돌지 않는다.
	if d.TaxSyncer != nil && !cfg.Rehearsal {
		yctx, ycancel := context.WithCancel(ctx)
		ydone := make(chan struct{})
		go func() { d.TaxSyncer.Run(yctx); close(ydone) }()
		defer func() { ycancel(); <-ydone }()
	}
	// 대행사 인증서 만료 알림(B43): 한 시간마다 30일·7일·당일 문턱을 점검한다. 복구 리허설 모드에서는 돌지 않는다.
	if d.TaxASP != nil && !cfg.Rehearsal {
		xctx2, xcancel2 := context.WithCancel(ctx)
		xdone2 := make(chan struct{})
		go func() { d.TaxASP.Run(xctx2); close(xdone2) }()
		defer func() { xcancel2(); <-xdone2 }()
	}
	// AI 비용 예산 한도 임박 알림(B49): 한 시간마다 이번 달 80%·100% 문턱을 점검한다. 복구 리허설 모드에서는 돌지 않는다.
	if runner, ok := d.AICost.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		cctx, ccancel := context.WithCancel(ctx)
		cdone := make(chan struct{})
		go func() { runner.Run(cctx); close(cdone) }()
		defer func() { ccancel(); <-cdone }()
	}
	// SCIM 동기화 기록 보존 정리(B40). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if d.SCIM != nil && !cfg.Rehearsal {
		sctx, scancel := context.WithCancel(ctx)
		sdone := make(chan struct{})
		go func() { d.SCIM.Run(sctx); close(sdone) }()
		defer func() { scancel(); <-sdone }()
	}
	if d.AuditAnchor != nil && !cfg.Rehearsal {
		// 주기 기록은 서버 프로세스만 한다(erp-mcp 는 하지 않는다). 종료 때 한 번 더 찍는다(DB 를 닫기 전).
		if r, ok := d.AuditAnchor.(interface {
			Run(context.Context, time.Duration)
		}); ok {
			go r.Run(ctx, cfg.AuditAnchorInterval)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := d.AuditAnchor.Stamp(sctx); err != nil {
				log.Printf("server: 종료 시 감사 앵커 기록 실패: %v", err)
			}
		}()
	}
	// 한도 초과 거부의 창 요약 감사 행을 분 단위로 정산한다. 종료 때는 정산을 마칠 때까지 기다린 뒤 DB 를 닫는다.
	if r, ok := d.MCPRate.(interface {
		Run(context.Context, time.Duration)
	}); ok && !cfg.Rehearsal {
		lctx, lcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { r.Run(lctx, time.Minute); close(done) }()
		defer func() { lcancel(); <-done }()
	}
	// 문서에 붙지 않은 채 24시간이 지난 첨부를 정리한다(B23). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if r, ok := d.Attachments.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		actx, acancel := context.WithCancel(ctx)
		adone := make(chan struct{})
		go func() { r.Run(actx); close(adone) }()
		defer func() { acancel(); <-adone }()
	}
	// 부재(B29): 예정 부재의 시작·종료 알림을 1분마다 만든다(부재의 효력은 이 작업과 무관하게 후보 계산이 기간으로 판정한다).
	if r, ok := d.Delegation.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		dctx, dcancel := context.WithCancel(ctx)
		ddone := make(chan struct{})
		go func() { r.Run(dctx); close(ddone) }()
		defer func() { dcancel(); <-ddone }()
	}
	// 근태·휴가(B50): 연차 발생·이월·소멸을 기동 때와 한 시간마다 따라잡는다(모듈이 꺼져 있으면 건너뛴다, 멱등). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다. erp-mcp 는 돌지 않는다.
	if r, ok := d.Leave.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		lvctx, lvcancel := context.WithCancel(ctx)
		lvdone := make(chan struct{})
		go func() { r.Run(lvctx); close(lvdone) }()
		defer func() { lvcancel(); <-lvdone }()
	}
	// 출퇴근(B50 5단계): 주 근로시간 경고(48시간 주의·52시간 경고)를 기동 때와 한 시간마다 평가한다(모듈이 꺼져 있으면 건너뛴다). erp-mcp 는 돌지 않는다.
	if r, ok := d.Attendance.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		atctx, atcancel := context.WithCancel(ctx)
		atdone := make(chan struct{})
		go func() { r.Run(atctx); close(atdone) }()
		defer func() { atcancel(); <-atdone }()
	}
	// 알림 채널(B36): 큐의 메일 발송 건을 5초마다 보낸다(업무 트랜잭션 밖). erp-mcp 는 이 작업을 하지 않는다.
	if d.Notify != nil && !cfg.Rehearsal {
		nctx, ncancel := context.WithCancel(ctx)
		ndone := make(chan struct{})
		go func() { d.Notify.Run(nctx); close(ndone) }()
		defer func() { ncancel(); <-ndone }()
	}
	// 주기 알림 이벤트(B36): 지급 예정·월 마감 점검을 영업일 업무 시작 뒤 만든다. erp-mcp 는 돌지 않는다.
	if d.NotifyEvents != nil {
		pctx, pcancel := context.WithCancel(ctx)
		pdone := make(chan struct{})
		go func() { d.NotifyEvents.Run(pctx); close(pdone) }()
		defer func() { pcancel(); <-pdone }()
	}
	// 법인카드(B32): 미소명 알림을 하루 한 번(KST 09:00 이후 첫 실행) 만든다.
	if r, ok := d.CardSvc.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		cctx, ccancel := context.WithCancel(ctx)
		cdone := make(chan struct{})
		go func() { r.Run(cctx); close(cdone) }()
		defer func() { ccancel(); <-cdone }()
	}
	// 실효 권한 캐시(B33): 기동 때와 날짜가 바뀔 때 전체 재계산과 대조해 어긋남을 바로잡는다(안전망). 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	if r, ok := d.Access.(interface{ Run(context.Context) }); ok && !cfg.Rehearsal {
		xctx, xcancel := context.WithCancel(ctx)
		xdone := make(chan struct{})
		go func() { r.Run(xctx); close(xdone) }()
		defer func() { xcancel(); <-xdone }()
	}
	// 조직도(B20): 날짜가 바뀌면 오늘 기준 투영을 맞춘다. 종료 때는 작업이 끝나기를 기다린 뒤 DB 를 닫는다.
	// erp-mcp 는 이 작업을 하지 않는다(퇴사 인증 거부는 인증 경로가 직접 판정한다).
	// dev IdP 가 켜진 개발·시연 환경에는 현실적인 규모의 조직을 한 번 넣고(조직 이벤트가 없을 때만, 기존 dev 계정 9개 포함),
	// 그 밖의 기존 부서·사용자는 한 번만 조직으로 옮긴다(멱등, 두 프로세스가 동시에 불러도 한 번만).
	if orgSvc, ok := d.Org.(*org.Service); ok && !cfg.Rehearsal {
		if cfg.DevIDP || cfg.DevSeedPassword != "" {
			if did, err := orgSvc.SeedDev(ctx); err != nil {
				return fmt.Errorf("개발용 조직 시드: %w", err)
			} else if did {
				log.Printf("server: 개발용 조직(본부 4, 부서 5, 팀 12, 약 60명)을 넣었습니다")
			}
		}
		if did, err := orgSvc.Backfill(ctx); err != nil {
			return fmt.Errorf("조직도 초기 반영: %w", err)
		} else if did {
			log.Printf("server: 기존 부서·사용자를 조직도로 옮겼습니다")
		}
		octx, ocancel := context.WithCancel(ctx)
		odone := make(chan struct{})
		go func() { orgSvc.Run(octx); close(odone) }()
		defer func() { ocancel(); <-odone }()
	}
	if !cfg.Rehearsal { // 리허설은 복구본에 아무것도 시드하지 않는다
		if err := seedDevPasswords(ctx, cfg, d); err != nil {
			return err
		}
	}
	// 설치 마법사(B38): 시드가 사용자를 만든 뒤에 설치 상태를 확정한다(사용자가 있으면 done). pending 이면 설치 코드를 정해 로그에 한 번 출력한다.
	if as, ok := d.Authenticator.(*auth.Service); ok {
		if _, err := setup.Reconcile(ctx, d.DB, nil); err != nil {
			return fmt.Errorf("설치 상태 확정: %w", err)
		}
		ss, err := setup.New(ctx, setup.Options{DB: d.DB, Txs: d.Txs, Audit: d.Audit, Auth: as, Code: cfg.SetupCode,
			Demo: func(ctx context.Context, adminID int64) error { return demo.SeedForInstall(ctx, d, adminID) }}) // 데모 시드·첫 관리자 배치는 설치 완료 커밋 뒤, 조립된 서비스(deps)로 부른다
		if err != nil {
			return fmt.Errorf("설치 마법사: %w", err)
		}
		d.Setup = ss
	}
	rt := buildRoutes(cfg, d)

	srv := &http.Server{Addr: cfg.Addr, Handler: handlerFor(rt, d), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("server: %s 에서 대기 (라우트 %d개)", cfg.Addr, len(rt.Routes()))

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
	return nil
}

// healthcheck 는 컨테이너 HEALTHCHECK 용이다(distroless 에는 curl 이 없다). 같은 주소의 /healthz 를 호출한다.
func healthcheck() int {
	cfg := config.Load()
	addr := cfg.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 2 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// seedDevPasswords 는 PL_DEV_SEED_PASSWORD 가 있을 때 개발 시드 계정(이메일 로그인 자격이 아직 없는 계정)에 초기 비밀번호 자격을 만든다.
// 개발 시드(사용자·조직)를 넣은 뒤에 불러야 한다. 값은 로그·감사 어디에도 남기지 않는다.
func seedDevPasswords(ctx context.Context, cfg config.Config, d deps.Deps) error {
	if cfg.DevSeedPassword == "" {
		return nil
	}
	svc, ok := d.Authenticator.(*auth.Service)
	if !ok {
		return nil
	}
	n, err := svc.SeedDevPasswords(ctx, cfg.DevSeedPassword)
	if err != nil {
		return fmt.Errorf("개발 시드 계정 비밀번호: %w", err)
	}
	if n > 0 {
		log.Printf("server: 개발 시드 계정 %d개에 이메일 로그인 자격을 만들었습니다(첫 로그인에서 비밀번호 변경을 강제합니다)", n)
	}
	return nil
}
