package notify

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 알림 채널 관리 라우트를 선언과 함께 등록한다.
// 발송 기록은 notification:manage 이고, 감사 담당은 같은 내용을 audit:read 경로(/api/audit/notify-deliveries)로 본다(라우트에는 권한 하나만 선언한다).
// 쓰기(다시 보내기·시험 메일)는 로그인한 화면에서만 부를 수 있다. 응답에는 메시지 본문·이메일 주소·웹훅 주소가 없다.
func (s *Service) Register(r contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, h) }
	m := contract.PermNotificationManage
	r.HandleFunc("GET", "/api/notify/status", m, s.hStatus)
	r.HandleFunc("GET", "/api/notify/deliveries", m, s.hDeliveries)
	r.HandleFunc("GET", "/api/audit/notify-deliveries", contract.PermAuditRead, s.hDeliveries)
	r.HandleFunc("POST", "/api/notify/deliveries/{id}/resend", m, so(s.hResend))
	r.HandleFunc("POST", "/api/notify/mail-test", m, so(s.hMailTest))
	// 웹훅 대상(단계 2). 응답에 주소·앱 키가 없다. 쓰기는 로그인한 화면에서만 부를 수 있다.
	r.HandleFunc("GET", "/api/notify/targets", m, s.hTargets)
	r.HandleFunc("POST", "/api/notify/targets", m, so(s.hTargetCreate))
	r.HandleFunc("PUT", "/api/notify/targets/{id}", m, so(s.hTargetUpdate))
	r.HandleFunc("POST", "/api/notify/targets/{id}/test", m, so(s.hTargetTest))
	r.HandleFunc("POST", "/api/notify/targets/{id}/pause", m, so(s.hTargetPause))
	r.HandleFunc("POST", "/api/notify/targets/{id}/resume", m, so(s.hTargetResume))
	r.HandleFunc("PUT", "/api/notify/hosts", m, so(s.hHosts))
	// 관리자 기본값·설정(단계 3).
	r.HandleFunc("GET", "/api/notify/defaults", m, s.hDefaults)
	r.HandleFunc("PUT", "/api/notify/defaults/{kind}", m, so(s.hSetDefault))
	r.HandleFunc("PUT", "/api/notify/settings", m, so(s.hSettings))
	// 내 수신 설정(전 역할, 본인 것만). 쓰기는 로그인한 화면에서만 부를 수 있다.
	r.HandleFunc("GET", "/api/notifications/prefs", contract.PermNotificationRead, s.hMyPrefs)
	r.HandleFunc("PUT", "/api/notifications/prefs/{kind}", contract.PermNotificationRead, so(s.hSetMyPref))
	r.HandleFunc("PUT", "/api/notifications/digest", contract.PermNotificationRead, so(s.hSetMyDigest))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		code, msg = http.StatusUnauthorized, err.Error()
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		code, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, contract.ErrNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, contract.ErrValidation):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, contract.ErrInvalidState):
		code, msg = http.StatusConflict, err.Error()
	default:
		log.Printf("notify: 내부 오류: %v", err)
	}
	http.Error(w, msg, code)
}

func (s *Service) hStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.QueueStatus(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Service) hDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := s.Deliveries(r.Context(), DeliveryFilter{Status: q.Get("status"), Channel: q.Get("channel"), Kind: q.Get("kind"), From: q.Get("from"), To: q.Get("to"), Limit: limit})
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": rows})
}

func (s *Service) hResend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "발송 건 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return
	}
	if err := s.Resend(r.Context(), id); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hMailTest(w http.ResponseWriter, r *http.Request) {
	if err := s.MailTest(r.Context()); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "대상 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		http.Error(w, "요청 형식이 올바르지 않습니다.", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Service) hTargets(w http.ResponseWriter, r *http.Request) {
	l, err := s.Targets(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Service) hTargetCreate(w http.ResponseWriter, r *http.Request) {
	var in TargetInput
	if !decodeBody(w, r, &in) {
		return
	}
	v, err := s.CreateTarget(r.Context(), in)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Service) hTargetUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var in TargetInput
	if !decodeBody(w, r, &in) {
		return
	}
	v, err := s.UpdateTarget(r.Context(), id, in)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hTargetTest(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	v, err := s.TestTarget(r.Context(), id)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hTargetPause(w http.ResponseWriter, r *http.Request)  { s.hSetActive(w, r, false) }
func (s *Service) hTargetResume(w http.ResponseWriter, r *http.Request) { s.hSetActive(w, r, true) }

func (s *Service) hSetActive(w http.ResponseWriter, r *http.Request, active bool) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	v, err := s.SetTargetActive(r.Context(), id, active)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hHosts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hosts []string `json:"hosts"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	h, err := s.SetExtraHosts(r.Context(), in.Hosts)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": h})
}

func (s *Service) hDefaults(w http.ResponseWriter, r *http.Request) {
	d, err := s.Defaults(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Service) hSetDefault(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
		Locked  bool `json:"locked"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.SetDefault(r.Context(), r.PathValue("kind"), in.Enabled, in.Locked); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Values map[string]string `json:"values"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.SetSettings(r.Context(), in.Values); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hMyPrefs(w http.ResponseWriter, r *http.Request) {
	p, err := s.MyPrefs(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) hSetMyPref(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.SetMyPref(r.Context(), r.PathValue("kind"), in.Mode); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hSetMyDigest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.SetMyDigest(r.Context(), in.Mode); err != nil {
		httpErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
