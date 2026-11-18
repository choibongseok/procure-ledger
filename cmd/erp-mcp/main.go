// 명령: erp-mcp — ERP 를 MCP 서버로 노출한다.
//
//	erp-mcp --transport stdio                 PL_MCP_TOKEN 의 개인 토큰으로 사용자를 확인한 뒤 표준 입출력으로 서비스
//	erp-mcp --transport http --addr :8090     streamable HTTP(/mcp). 호출마다 Authorization: Bearer <개인 토큰>
//
// 서버 바이너리(cmd/server)도 같은 핸들러를 POST /mcp 로 마운트한다. 서비스 조립은 둘 다 internal/bootstrap.Build 를 쓰고
// 같은 환경변수(PL_DB_PATH 등)로 같은 SQLite 파일을 연다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/mcp"
)

// buildDeps 는 서버와 같은 조립(internal/bootstrap.Build)이다. 조립을 따로 만들지 않는다.
// DB 는 PL_DB_PATH(서버와 같은 SQLite 파일)를 연다. 반환 함수는 DB 를 닫는다.
var buildDeps = func(ctx context.Context) (deps.Deps, func(), error) {
	return bootstrap.Build(ctx, config.Load())
}

// rateLimitWarning 은 분당 한도가 둘 다 꺼져 있을 때의 경고문이다. 켜져 있으면 빈 문자열이다.
func rateLimitWarning(d deps.Deps) string {
	if e, ok := d.MCPRate.(interface{ Enabled() bool }); ok && e.Enabled() {
		return ""
	}
	if d.MCPRate != nil && d.MCPRate.Limit() > 0 {
		return ""
	}
	return "erp-mcp: 경고: AI 연결 분당 호출 한도가 꺼져 있습니다(PL_MCP_RATE_PER_MIN=0, PL_MCP_RATE_PER_USER_PER_MIN=0). 호출 폭주를 막으려면 한도를 설정하세요."
}

// settle 은 지난 한도 창의 거부 요약 감사 기록을 정산한다. 실패해도 서비스에는 영향이 없다.
func settle(d deps.Deps, errw io.Writer) {
	s, ok := d.MCPRate.(interface {
		Settle(context.Context) (int, error)
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Settle(ctx); err != nil {
		fmt.Fprintln(errw, "erp-mcp: 한도 거부 요약 정산 실패:", err)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}

func run(ctx context.Context, args []string, errw io.Writer) int {
	fs := flag.NewFlagSet("erp-mcp", flag.ContinueOnError)
	fs.SetOutput(errw)
	transport := fs.String("transport", "stdio", "stdio | http")
	addr := fs.String("addr", ":8090", "http 전송의 대기 주소")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintf(errw, "알 수 없는 전송 %q (stdio|http)\n", *transport)
		return 2
	}
	d, closeFn, err := buildDeps(ctx)
	if err != nil {
		fmt.Fprintln(errw, "erp-mcp:", err)
		return 1
	}
	defer closeFn()
	// 한도 설정은 서버와 같은 config.Load()/조립이 정한다(여기서 따로 읽지 않는다). 꺼져 있으면 경고한다. stdout 은 프로토콜 채널이라 stderr 로만 낸다.
	if w := rateLimitWarning(d); w != "" {
		fmt.Fprintln(errw, w)
	}
	// 지난 창의 거부 요약을 기동 때와 종료 때 정산한다(프로세스가 죽으며 못 남긴 요약을 다음 기동이 잇는다).
	settle(d, errw)
	defer settle(d, errw)

	switch *transport {
	case "stdio":
		if err := mcp.RunStdio(ctx, d, os.Getenv("PL_MCP_TOKEN")); err != nil {
			fmt.Fprintln(errw, "erp-mcp:", err)
			return 1
		}
	case "http":
		if r, ok := d.MCPRate.(interface {
			Run(context.Context, time.Duration)
		}); ok { // http 전송은 오래 떠 있으므로 분 단위로 정산한다
			lctx, lcancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { r.Run(lctx, time.Minute); close(done) }()
			defer func() { lcancel(); <-done }()
		}
		mux := http.NewServeMux()
		mux.Handle("/mcp", mcp.NewHTTPHandler(d))
		srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(errw, "erp-mcp:", err)
			return 1
		}
	}
	return 0
}
