// 조립 지점. 이 파일은 platform 소유이며 세 영역이 공유하지 않는다.
//
// 영역별 편집 파일(서로 겹치지 않는다)
//
//	routes_platform.go / internal/bootstrap/wire_platform.go   platform
//	routes_erp.go      / internal/bootstrap/wire_erp.go        erp
//	routes_ai.go       / internal/bootstrap/wire_ai.go         ai
//
// routes_*.go 는 해당 영역의 handler.Register* 호출만 담는다. 서비스 조립(설정 → DB·마이그레이션 →
// platform → erp → ai)은 internal/bootstrap.Build 한 벌이고 cmd/server 와 cmd/erp-mcp 가 같이 쓴다.
// wire_*.go 는 해당 영역 서비스를 만들어 deps.Deps 의 자기 필드에 채운다. app.go·main.go 는 영역 작업 중 고칠 일이 없다.
package main

import (
	"github.com/choibongseok/procure-ledger/internal/apiv1"
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/scim"
)

// buildRoutes 는 모든 영역의 라우트를 하나의 RouteTable 로 조립한다.
func buildRoutes(cfg config.Config, d deps.Deps) *contract.RouteTable {
	rt := contract.NewRouteTable()
	// 권한 강제 + 권한 거부 감사 기록(platform). d.Audit·d.Txs 는 wirePlatform 이 채운다.
	rt.Guard = rbac.NewGuard(d.Audit, d.Txs)
	registerPlatform(rt, cfg, d)
	registerERP(rt, d)
	registerAI(rt, d)
	return rt
}

// handler 는 인증 미들웨어까지 감싼 최종 http.Handler 다.
// 인증 미들웨어(세션·토큰 → Principal 을 context 에 주입)는 platform 이 wrapAuth 에서 구현한다.
func handlerFor(rt *contract.RouteTable, d deps.Deps) http.Handler {
	base := wrapAuth(rt.Handler(), d)
	var scimH, apiH http.Handler
	if d.SCIM != nil {
		// /scim/v2 는 SCIM 연동 토큰(pls_)으로만 인증한다. 쿠키·세션·개인 토큰 인증과 섞이지 않게 경로를 나눠 보낸다(B40).
		scimH = d.SCIM.Wrap(rt.Handler())
	}
	if d.APIv1 != nil {
		// /api/v1 은 연동 API 토큰(pli_)으로만 인증한다(B42). 쿠키·세션·개인 토큰·SCIM 토큰은 여기서 인정하지 않고, 반대로 pli_ 토큰은 다른 경로에서 인정하지 않는다.
		// apitoken.Wrap 이 동시 요청 상한·호출 요약·범위 밖 거부 감사를 맡고, apiv1.Wrap 이 인증기(토큰 확인·속도 제한)를 부른다.
		apiH = d.APIv1.Wrap(rt.Handler())
		if d.APITokens != nil {
			apiH = d.APITokens.Wrap(apiH)
		}
	}
	if scimH == nil && apiH == nil {
		return base
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case scimH != nil && scim.IsSCIMPath(r.URL.Path):
			scimH.ServeHTTP(w, r)
		case apiH != nil && apiv1.IsAPIPath(r.URL.Path):
			apiH.ServeHTTP(w, r)
		default:
			base.ServeHTTP(w, r)
		}
	})
}

// wrapAuth 는 세션 쿠키·개인 토큰으로 Principal 을 만들어 context 에 넣는 미들웨어다(CSRF 검사 포함).
// d.AuthHTTP 가 없으면(가짜 의존성 테스트) 아무것도 하지 않는다 = 모든 요청이 미인증.
func wrapAuth(next http.Handler, d deps.Deps) http.Handler {
	if d.AuthHTTP == nil {
		return next
	}
	return d.AuthHTTP.Wrap(next)
}
