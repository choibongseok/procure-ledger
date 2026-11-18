package onboarding

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Register 는 점검표·백업 상태 라우트를 선언과 함께 등록한다. 둘 다 읽기 위주이고 백업·복구를 실행하는 경로는 없다(D16).
func (s *Service) Register(r contract.Registrar) {
	r.HandleFunc("GET", "/api/admin/onboarding", contract.PermOnboardingRead, s.handleList)
	r.HandleFunc("POST", "/api/admin/onboarding/skips", contract.PermOnboardingRead, s.handleSkip)
	r.HandleFunc("DELETE", "/api/admin/onboarding/skips/{item}", contract.PermOnboardingRead, s.handleUnskip)
	r.HandleFunc("GET", "/api/admin/backup/status", contract.PermBackupRead, s.handleBackup)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "알 수 없는 점검 항목입니다"})
	case errors.Is(err, contract.ErrValidation):
		msg := err.Error()
		if i := len(contract.ErrValidation.Error()) + 2; len(msg) > i {
			msg = msg[i:]
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
	default:
		log.Printf("onboarding: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	items, sum, err := s.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "summary": sum})
}

func (s *Service) handleSkip(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Item   string `json:"item"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return
	}
	if err := s.MarkNotApplicable(r.Context(), in.Item, in.Reason); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUnskip(w http.ResponseWriter, r *http.Request) {
	if err := s.ClearNotApplicable(r.Context(), r.PathValue("item")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleBackup(w http.ResponseWriter, r *http.Request) {
	st, err := s.BackupStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
