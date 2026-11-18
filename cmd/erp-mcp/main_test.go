package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/mcpusage"
)

func TestRejectsUnknownTransport(t *testing.T) {
	if code := run(context.Background(), []string{"--transport", "ws"}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("exit=%d", code)
	}
}

func TestReportsAssemblyFailure(t *testing.T) {
	orig := buildDeps
	defer func() { buildDeps = orig }()
	buildDeps = func(context.Context) (deps.Deps, func(), error) {
		return deps.Deps{}, func() {}, errors.New("조립 실패")
	}
	var errw bytes.Buffer
	if code := run(context.Background(), []string{"--transport", "stdio"}, &errw); code != 1 || !strings.Contains(errw.String(), "조립 실패") {
		t.Fatalf("조립 실패는 종료 코드 1: exit=%d %q", code, errw.String())
	}
}

// 한도가 꺼져 있으면 기동 때 stderr 에 경고를 낸다(stdout 은 프로토콜 채널이라 쓰지 않는다).
func TestWarnsWhenRateLimitDisabled(t *testing.T) {
	orig := buildDeps
	defer func() { buildDeps = orig }()
	buildDeps = func(context.Context) (deps.Deps, func(), error) { return deps.Deps{}, func() {}, nil }
	t.Setenv("PL_MCP_TOKEN", "") // 토큰이 없으면 stdio 가 곧바로 실패하므로 서버는 뜨지 않는다
	var errw bytes.Buffer
	run(context.Background(), []string{"--transport", "stdio"}, &errw)
	if !strings.Contains(errw.String(), "분당 호출 한도가 꺼져 있습니다") {
		t.Fatalf("경고가 없다: %q", errw.String())
	}
}

func TestNoWarningWhenAnyRateLimitEnabled(t *testing.T) {
	if rateLimitWarning(deps.Deps{MCPRate: mcpusage.NewRateLimiter(nil, 5, 0)}) != "" {
		t.Fatal("토큰 한도가 있으면 경고하지 않는다")
	}
	if rateLimitWarning(deps.Deps{MCPRate: mcpusage.NewRateLimiter(nil, 0, 9)}) != "" {
		t.Fatal("사용자 합산 한도만 있어도 경고하지 않는다")
	}
	if rateLimitWarning(deps.Deps{}) == "" {
		t.Fatal("한도가 없으면 경고한다")
	}
}
