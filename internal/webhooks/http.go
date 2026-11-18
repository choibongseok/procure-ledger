package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// Register 는 웹훅 관리 화면 API 를 선언과 함께 등록한다(B42 명세 9.7). 변경은 로그인 세션만(개인 토큰·API 토큰은 403)이다.
// 발송 기록 조회는 webhook:manage 또는 audit:read 라서 선언은 로그인만 두고 서비스가 판정한다(거부는 route 계층과 같은 형태의 거부 감사를 남긴다).
func (s *Service) Register(reg contract.Registrar) {
	so := func(h http.HandlerFunc) http.HandlerFunc { return rbac.SessionOnly(s.o.Audit, s.o.Txs, h) }
	m := s.o.ManagePerm
	reg.HandleFunc("GET", "/api/admin/integrations/webhooks", m, s.hList)
	reg.HandleFunc("GET", "/api/admin/integrations/webhook-events", m, s.hEvents)
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks", m, so(s.hCreate))
	reg.HandleFunc("PUT", "/api/admin/integrations/webhooks/{id}", m, so(s.hUpdate))
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks/{id}/pause", m, so(s.hPause(true)))
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks/{id}/resume", m, so(s.hPause(false)))
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks/{id}/rotate-secret", m, so(s.hRotate))
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks/{id}/test", m, so(s.hTest))
	reg.HandleFunc("POST", "/api/admin/integrations/webhooks/{id}/resend-failed", m, so(s.hResendFailed))
	reg.HandleFunc("GET", "/api/admin/integrations/webhook-deliveries", contract.PermAuthenticated, s.hDeliveries)
	reg.HandleFunc("POST", "/api/admin/integrations/webhook-deliveries/{id}/resend", m, so(s.hResend))
	reg.HandleFunc("GET", "/api/admin/integrations/webhook-hosts", m, s.hGetHosts)
	reg.HandleFunc("PUT", "/api/admin/integrations/webhook-hosts", m, so(s.hSetHosts))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	msg := func(prefix error) string { return strings.TrimPrefix(err.Error(), prefix.Error()+": ") }
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg(contract.ErrInvalidState)})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg(contract.ErrValidation)})
	default:
		log.Printf("webhooks: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, valErr("요청 형식이 올바르지 않습니다"))
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	out, err := s.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if out == nil {
		out = []View{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) hEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, EventTypes())
}

func (s *Service) hCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string   `json:"name"`
		URL        string   `json:"url"`
		Events     []string `json:"events"`
		IncludeAPI bool     `json:"includeApi"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Create(r.Context(), CreateInput{Name: in.Name, URL: in.URL, Events: in.Events, IncludeAPI: in.IncludeAPI})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) hUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       *string  `json:"name"`
		URL        *string  `json:"url"`
		Events     []string `json:"events"`
		IncludeAPI *bool    `json:"includeApi"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Update(r.Context(), id, UpdateInput{Name: in.Name, URL: in.URL, Events: in.Events, IncludeAPI: in.IncludeAPI})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) hPause(pause bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var v View
		var err error
		if pause {
			v, err = s.Pause(r.Context(), id)
		} else {
			v, err = s.Resume(r.Context(), id)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Service) hRotate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret, err := s.RotateSecret(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (s *Service) hTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	msg, err := s.SendTest(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": msg == "", "message": msg})
}

func (s *Service) hResendFailed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := s.ResendFailed(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (s *Service) hResend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Resend(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) hGetHosts(w http.ResponseWriter, r *http.Request) {
	h, err := s.GetHosts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Service) hSetHosts(w http.ResponseWriter, r *http.Request) {
	var in Hosts
	if !decode(w, r, &in) {
		return
	}
	if err := s.SetHosts(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dayStart(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", v, time.FixedZone("KST", 9*3600))
}

func (s *Service) hDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f DeliveryFilter
	f.SubID, _ = strconv.ParseInt(q.Get("sub"), 10, 64)
	f.Status, f.EventType = q.Get("status"), q.Get("type")
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	var err error
	if f.From, err = dayStart(q.Get("from")); err != nil {
		writeErr(w, valErr("시작 날짜 형식이 올바르지 않습니다"))
		return
	}
	to, err := dayStart(q.Get("to"))
	if err != nil {
		writeErr(w, valErr("종료 날짜 형식이 올바르지 않습니다"))
		return
	}
	if !to.IsZero() {
		f.To = to.AddDate(0, 0, 1)
	}
	out, err := s.AllDeliveries(r.Context(), f)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			s.denyAudit(r)
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// denyAudit 는 서비스가 발송 기록 조회를 거부했을 때 route 계층과 같은 형태의 거부 감사(access.denied)를 남긴다.
func (s *Service) denyAudit(r *http.Request) {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		return
	}
	target := r.Pattern
	if target == "" {
		target = r.Method + " " + r.URL.Path
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "access.denied", TargetType: audit.TargetRoute, TargetID: target, Result: audit.ResultDenied,
			Detail: "필요 권한 " + string(s.o.ManagePerm) + " 또는 " + string(contract.PermAuditRead)})
	}); err != nil {
		log.Printf("webhooks: 거부 기록 실패: %v", err)
	}
}

// Run 은 서버 주기 작업이다: 2초마다 Tick(피드 읽기·발송). 복구 리허설 모드에서는 서버가 돌리지 않는다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("webhooks: 발송 작업 실패: %v", err)
		}
	}
}
