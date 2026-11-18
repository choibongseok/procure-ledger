package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/config"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/handler"
	"github.com/choibongseok/procure-ledger/web"
)

// registerPlatform 은 platform 라우트(헬스체크, 정적 프론트, 인증, 관리)를 등록한다.
// OIDC 로그인·세션·개인 토큰 라우트는 platform 이 여기에 추가한다(Public 은 명시적으로 선언).
func registerPlatform(r contract.Registrar, cfg config.Config, d deps.Deps) {
	r.HandleFunc("GET", "/healthz", contract.PermPublic, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	if d.AuthHTTP != nil { // 로그인·콜백·로그아웃·내 정보, 설정으로 켠 경우 dev IdP
		d.AuthHTTP.Register(r)
	}
	if d.Setup != nil { // 설치 마법사(B38): 설치 코드로 보호하는 공개 경로, 설치가 끝나면 모두 404
		d.Setup.Register(r)
	}
	if d.Onboarding != nil { // 도입 점검표·백업 상태(B38 3단계, 읽기 위주)
		d.Onboarding.Register(r)
	}
	if d.Entities != nil { // 법인·사업장 관리 화면 API(B44)
		d.Entities.Register(r)
	}
	if d.Modules != nil { // 업무 모듈 켜기·끄기 화면 API(B50)
		d.Modules.Register(r)
	}
	if d.Webhooks != nil { // 변경 웹훅 구독·발송 기록 화면 API(B42 2단계)
		d.Webhooks.Register(r)
	}
	if d.TaxASP != nil { // 전자세금계산서 대행사 설정 화면 API(B43)
		d.TaxASP.Register(r)
	}
	if d.APITokens != nil { // 연동 API 토큰 관리 화면 API(B42)
		d.APITokens.Register(r)
	}
	if d.APIv1 != nil { // /api/v1 읽기 라우트(B42). 인증은 handlerFor 가 경로를 나눠 pli_ 토큰으로만 건다
		d.APIv1.Register(r)
	}
	if d.PrivReport != nil { // 월별 점검 보고서 내려받기(B41)
		d.PrivReport.Register(r)
		if d.Export != nil { // 조회 기록 목록 내보내기(B41 명세 5.5)
			d.PrivReport.RegisterExport(r, d.Export)
		}
	}
	if reg, ok := d.SoD.(interface{ Register(contract.Registrar) }); ok { // 권한 분리 규칙·위반·예외 화면 API(B41)
		reg.Register(r)
	}
	if reg, ok := d.Privacy.(interface{ Register(contract.Registrar) }); ok { // 개인정보 조회 기록 화면 API(B41)
		reg.Register(r)
	}
	if d.SCIM != nil { // 계정 자동 동기화(B40): /scim/v2 와 관리 API
		d.SCIM.Register(r)
	}
	if d.Access != nil { // 역할·부여 관리(B33)
		d.Access.Register(r)
	}
	if d.Notify != nil { // 알림 채널 관리(B36)
		d.Notify.Register(r)
	}
	if reg, ok := d.Import.(interface{ Register(contract.Registrar) }); ok { // 기초 데이터 가져오기(B35)
		reg.Register(r)
	}
	handler.RegisterAdmin(r, d)
	handler.RegisterOrg(r, d)     // 조직도·인사 발령(B20)
	handler.RegisterExports(r, d) // 감사 로그·조직 구성원 내보내기(B28). 다른 목록은 각 영역이 export.Register 로 붙는다

	// 프론트(SPA) 정적 서빙. API 가 아닌 모든 GET 경로는 index.html 로 떨어진다. 데이터가 없는 공개 자원이다.
	spa := spaHandler(web.Dist())
	if d.Setup != nil { // 마법사 화면은 설치 중에만 존재한다(끝나면 404)
		r.Handle("GET", "/setup", contract.PermPublic, d.Setup.PageHandler(spa))
	}
	r.Handle("GET", "/{path...}", contract.PermPublic, spa)
}

// spaHandler 는 정적 파일이 있으면 그대로, 없으면 index.html 을 돌려준다.
func spaHandler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/scim/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(dist, name); err != nil {
			if _, ierr := fs.Stat(dist, "index.html"); ierr != nil {
				http.Error(w, "프론트가 빌드되지 않았습니다 (make build)", http.StatusServiceUnavailable)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		files.ServeHTTP(w, r)
	})
}
