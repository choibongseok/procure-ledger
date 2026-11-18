package main

import (
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/handler"
	"github.com/choibongseok/procure-ledger/internal/mcp"
)

// registerAI 는 ai 라우트(문서 판정, LLM 사용량·정책, MCP)를 등록한다. 새 Register 함수는 여기에만 추가한다.
//
// /mcp 는 streamable HTTP MCP 엔드포인트다. 세션 쿠키가 아니라 Authorization: Bearer 개인 토큰으로
// 호출마다 사용자를 확인하므로(mcp.NewHTTPHandler 가 Authenticator 로 검증, 실패 시 401) 라우트 가드는 열어 두고
// 인증·권한은 핸들러와 도구 가드가 강제한다. 도구 권한은 contract.MCPToolPermissions 표로 판정한다.
func registerAI(r contract.Registrar, d deps.Deps) {
	handler.RegisterDocVerify(r, d)
	handler.RegisterLLM(r, d)
	handler.RegisterLLMProviders(r, d)
	if d.EvalSets != nil {
		d.EvalSets.Register(r)
	}
	if d.MCPTools != nil {
		d.MCPTools.Register(r)
	}
	if d.AICost != nil {
		d.AICost.Register(r)
	}
	handler.RegisterPolicy(r, d)
	handler.RegisterKnowledge(r, d) // B47 사내 지식 검색·색인 관리
	r.Handle("POST", "/mcp", contract.PermPublic, mcp.NewHTTPHandler(d))
}
