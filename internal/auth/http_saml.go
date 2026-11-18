package auth

import (
	"errors"
	"log"
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// registerSAML 은 SAML SP 라우트를 등록한다(모두 공개: 인증은 서명된 응답과 우리가 시작한 요청 ID 가 한다).
// ACS 는 교차 사이트 POST 라 SameSite 쿠키가 막지 못하므로 InResponseTo·RelayState(1회용)로 막는다(명세 9.5).
func (s *Service) registerSAML(r contract.Registrar) {
	r.HandleFunc("GET", "/api/auth/saml/login", contract.PermPublic, s.handleSAMLLogin)
	r.HandleFunc("POST", "/api/auth/saml/acs", contract.PermPublic, s.handleSAMLACS)
	r.HandleFunc("GET", "/api/auth/saml/metadata", contract.PermPublic, s.handleSAMLMetadata)
}

func (s *Service) handleSAMLLogin(w http.ResponseWriter, r *http.Request) {
	u, err := s.beginSAML(r.Context(), r.URL.Query().Get("returnTo"), purposeLogin, 0)
	if err != nil {
		if errors.Is(err, ErrLoginNotConfigured) {
			http.Error(w, err.Error(), http.StatusNotFound) // 꺼진 방식은 없는 것처럼
			return
		}
		log.Printf("auth: SAML 로그인 시작 실패: %v", err)
		http.Redirect(w, r, "/login?error=idp", http.StatusFound)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Service) handleSAMLMetadata(w http.ResponseWriter, _ *http.Request) {
	b, err := s.SAMLMetadata()
	if err != nil {
		http.Error(w, "SAML 이 설정되지 않았습니다", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(b)
}

func (s *Service) handleSAMLACS(w http.ResponseWriter, r *http.Request) {
	fail := func(code string) { http.Redirect(w, r, "/login?error="+code, http.StatusSeeOther) }
	r.Body = http.MaxBytesReader(w, r.Body, samlResponseMax*2)
	res, err := s.completeSAML(r.Context(), r)
	switch {
	case err == nil:
	case errors.Is(err, contract.ErrForbidden):
		fail("forbidden")
		return
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrLoginNotConfigured):
		fail("state")
		return
	default:
		log.Printf("auth: SAML 처리 실패: %v", err)
		fail("idp")
		return
	}
	if res.Purpose == purposeTest {
		if err := s.finishTest(r.Context(), res); err != nil {
			log.Printf("auth: SAML 시험 기록 실패: %v", err)
			fail("server")
			return
		}
		http.Redirect(w, r, res.Redirect, http.StatusSeeOther)
		return
	}
	prev := rawSessionCookie(r)
	raw, expires, err := s.loginWithPrev(r.Context(), res.Identity, prev)
	switch {
	case err == nil:
	case errors.Is(err, contract.ErrForbidden):
		fail("forbidden")
		return
	default:
		log.Printf("auth: SAML 로그인 실패: %v", err)
		fail("server")
		return
	}
	s.setSessionCookie(w, raw, expires)
	http.Redirect(w, r, res.Redirect, http.StatusSeeOther)
}
