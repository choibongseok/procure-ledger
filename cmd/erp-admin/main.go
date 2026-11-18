// 명령: erp-admin — 서버 호스트의 운영 명령(B38 D12).
//
//	erp-admin access-recompute        실효 권한 캐시 전체 재계산
//	erp-admin auth-unlock <이메일>    계정 잠금 해제·이메일 로그인 복구
//
// 구현은 internal/admincmd 에 있고 `server access-recompute`·`server auth-unlock` 이 같은 구현을 부르는 별칭이다.
// 외부 프로그램에 의존하지 않는 단일 정적 바이너리다(컨테이너 이미지 /app/erp-admin). 서버 호스트의 사용자만 실행한다.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/choibongseok/procure-ledger/internal/admincmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := admincmd.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
