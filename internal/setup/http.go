package setup

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// CodeHeader 는 설치 코드를 싣는 요청 헤더다. 코드는 URL·쿠키·응답 어디에도 쓰지 않는다.
const CodeHeader = "X-Setup-Code"

const maxBody = 64 << 10

// Register 는 마법사 라우트를 선언과 함께 등록한다. 모두 권한 대신 설치 코드로 보호하는 공개 경로(PermPublic)이고,
// 설치가 끝나면(done) 전부 404 다 — 마법사 경로의 존재를 숨긴다.
func (s *Service) Register(r contract.Registrar) {
	r.HandleFunc("GET", "/api/setup/status", contract.PermPublic, s.handleStatus)
	r.HandleFunc("POST", "/api/setup/check", contract.PermPublic, s.guarded(s.handleCheck))
	r.HandleFunc("POST", "/api/setup/validate", contract.PermPublic, s.guarded(s.handleValidate))
	r.HandleFunc("POST", "/api/setup/complete", contract.PermPublic, s.guarded(s.handleComplete))
}

// PageHandler 는 `/setup` 화면 요청을 감싼다: 설치가 끝났으면 404, 아니면 next(SPA).
func (s *Service) PageHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Pending(r.Context()) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type body struct {
	CompanyName      string `json:"companyName"`
	BusinessNo       string `json:"businessNo"`
	Representative   string `json:"representative"`
	Address          string `json:"address"`
	Phone            string `json:"phone"`
	FiscalStartMonth int    `json:"fiscalStartMonth"`
	AdminName        string `json:"adminName"`
	AdminEmail       string `json:"adminEmail"`
	AdminPassword    string `json:"adminPassword"`
	Department       string `json:"department"`
	Demo             bool   `json:"demo"`
	DemoConfirm      string `json:"demoConfirm"`
}

func (b body) input() Input {
	return Input{
		Company:          domain.CompanyInput{Name: b.CompanyName, BusinessNo: b.BusinessNo, Representative: b.Representative, Address: b.Address, Phone: b.Phone},
		FiscalStartMonth: b.FiscalStartMonth, AdminName: b.AdminName, AdminEmail: b.AdminEmail, AdminPassword: b.AdminPassword,
		Department: b.Department, Demo: b.Demo, DemoConfirmCompany: b.DemoConfirm,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.Pending(r.Context()) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": StatusPending}) // 버전·DB 종류·사용자 수 등은 싣지 않는다
}

// guarded 는 모든 POST 의 공통 검사다: 설치 중인가(아니면 404) → 같은 출처·JSON → 설치 코드(상수 시간 비교, 전역 시도 제한).
func (s *Service) guarded(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Pending(r.Context()) {
			http.NotFound(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "허용되지 않은 요청 출처입니다"})
				return
			}
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "JSON 요청이어야 합니다"})
			return
		}
		err := s.CheckCode(r.Context(), r.Header.Get(CodeHeader))
		var le LockedError
		switch {
		case err == nil:
			next(w, r)
		case errors.As(err, &le):
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(le.Until).Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": le.Error()})
		case errors.Is(err, ErrExpired):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": ErrBadCode.Error()}) // 힌트 없음
		}
	}
}

func (s *Service) handleCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "totpKeyConfigured": s.TOTPKeyConfigured()})
}

func readBody(w http.ResponseWriter, r *http.Request) (body, bool) {
	var b body
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return b, false
	}
	return b, true
}

func (s *Service) handleValidate(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	in := b.input()
	errs := s.Validate(&in)
	writeJSON(w, http.StatusOK, map[string]any{"ok": errs == nil, "errors": errs})
}

func (s *Service) handleComplete(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	res, err := s.Complete(r.Context(), b.input())
	var fe FieldErrors
	switch {
	case err == nil:
		// 세션은 만들지 않는다. 화면이 로그인 화면으로 이동한다. 데모 데이터 실패는 설치를 되돌리지 않고 안내만 보낸다(원인은 서버 로그에만 있다).
		out := map[string]any{"done": true}
		if res.DemoErr != nil {
			out["demoFailed"] = true
		}
		writeJSON(w, http.StatusCreated, out)
	case errors.Is(err, ErrAlreadyDone):
		writeJSON(w, http.StatusConflict, map[string]string{"error": ErrAlreadyDone.Error()})
	case errors.As(err, &fe):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "입력을 확인하세요", "errors": fe})
	case errors.Is(err, contract.ErrAuditFailed):
		log.Printf("setup: 완료 감사 기록 실패: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	default:
		log.Printf("setup: 완료 처리 실패: %v", err) // 입력값(이메일·비밀번호)은 로그에 넣지 않는다
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}
