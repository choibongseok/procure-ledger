// Package admincmd 는 서버 호스트의 운영 명령이다(B38 D12).
//
// 바이너리 cmd/erp-admin 이 주 진입점이고, 기존 `server access-recompute`·`server auth-unlock` 은 같은 구현을 부르는 호환 별칭이다
// (기존 문서·스크립트가 깨지지 않게). 모든 명령은 DB 에 접근할 수 있는 호스트 사용자가 곧 권한이라는 전제이며 웹에서는 실행할 수 없다.
// 감사 기록은 경로 cli 로 같은 해시 체인에 남는다. 서비스 조립은 서버·erp-mcp 와 같은 bootstrap.Build 다.
package admincmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/bootstrap"
	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
)

// Actor 는 명령줄 작업의 감사 행위자다(사용자 없는 시스템 작업, 경로 cli).
var Actor = contract.Principal{Role: "system", Channel: contract.ChannelCLI}

// Build 는 의존성을 만든다. 시험이 바꿔 끼운다.
var Build = func(ctx context.Context) (deps.Deps, func(), error) {
	return bootstrap.Build(ctx, config.Load())
}

type command struct {
	name, usage, help string
	run               func(ctx context.Context, args []string, out io.Writer) error
}

var commands = []command{
	{"access-recompute", "access-recompute", "실효 권한 캐시를 부여·역할 권한·오늘 조직도에서 처음부터 다시 계산한다(감사 role.recompute).", runAccessRecompute},
	{"auth-unlock", "auth-unlock <이메일>", "계정 잠금을 풀고, 꺼진 이메일 로그인을 다시 켠다(감사 auth.account_unlock).", runAuthUnlock},
}

// Names 는 명령 이름들이다(서버 별칭 목록·시험용).
func Names() []string {
	var n []string
	for _, c := range commands {
		n = append(n, c.name)
	}
	return n
}

// Run 은 명령을 실행하고 종료 코드를 돌려준다. args 는 명령 이름부터다.
func Run(ctx context.Context, args []string, out, errw io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(out)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	for _, c := range commands {
		if c.name == args[0] {
			if err := c.run(ctx, args[1:], out); err != nil {
				fmt.Fprintf(errw, "erp-admin: %v\n", err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(errw, "erp-admin: 알 수 없는 명령 %q\n\n", args[0])
	usage(errw)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "사용법: erp-admin <명령> [인자]")
	fmt.Fprintln(w, "서버와 같은 환경변수(PL_DB_PATH 또는 PL_DB_URL 등)로 같은 DB 를 연다. 서버 호스트에서만 실행한다.")
	fmt.Fprintln(w)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-28s %s\n", c.usage, c.help)
	}
}

func runAccessRecompute(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("사용법: access-recompute")
	}
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	svc, ok := d.Access.(*access.Service)
	if !ok {
		return errors.New("역할 서비스를 만들지 못했습니다")
	}
	n, err := svc.Reconcile(ctx, Actor, true)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "실효 권한 캐시를 다시 계산했습니다(바뀐 행 %d)\n", n)
	return nil
}

func runAuthUnlock(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("사용법: auth-unlock <이메일>")
	}
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	svc, ok := d.Authenticator.(*auth.Service)
	if !ok {
		return errors.New("인증 서비스를 만들지 못했습니다")
	}
	done, err := svc.CLIUnlock(ctx, args[0])
	if err != nil {
		return err
	}
	for _, line := range done {
		fmt.Fprintln(out, line)
	}
	return nil
}
