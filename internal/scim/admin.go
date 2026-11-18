package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// registerAdmin 은 관리자 화면용 API 를 등록한다. 토큰·설정 변경은 로그인 세션 전용이다(Bearer 로는 403).
func (s *Service) registerAdmin(r contract.Registrar) {
	s.registerGroupAdmin(r)
	r.HandleFunc("GET", "/api/admin/scim", contract.PermSCIMRead, s.handleOverview)
	r.HandleFunc("GET", "/api/admin/scim/log", contract.PermSCIMRead, s.handleLog)
	r.HandleFunc("PUT", "/api/admin/scim/settings", contract.PermSCIMManage, rbac.SessionOnly(s.audit, s.txs, s.handleSaveSettings))
	r.HandleFunc("POST", "/api/admin/scim/tokens", contract.PermSCIMManage, rbac.SessionOnly(s.audit, s.txs, s.handleIssue))
	r.HandleFunc("DELETE", "/api/admin/scim/tokens/{id}", contract.PermSCIMManage, rbac.SessionOnly(s.audit, s.txs, s.handleRevoke))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "로그인이 필요합니다"})
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSetupRequired):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "권한이 없습니다"})
	case errors.Is(err, contract.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, contract.ErrValidation):
		msg := strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": ")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	default:
		logf("scim: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func principal(w http.ResponseWriter, r *http.Request) (contract.Principal, bool) {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		writeAdminErr(w, contract.ErrUnauthenticated)
	}
	return p, ok
}

func (s *Service) handleOverview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	st, err := s.GetSettings(r.Context(), p)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	toks, err := s.List(r.Context(), p)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoint": s.BaseEndpoint(), "settings": st, "tokens": toks, "maxActiveTokens": s.maxToken, "sources": Sources, "scopes": Scopes})
}

func (s *Service) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in Settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return
	}
	out, err := s.SaveSettings(r.Context(), p, in)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleIssue(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Scope  string `json:"scope"`
		Days   int    `json:"days"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return
	}
	raw, info, err := s.Issue(r.Context(), p, in.Name, in.Source, in.Scope, time.Duration(in.Days)*24*time.Hour)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	// 원문은 이 응답에서만 보인다(저장되지 않는다).
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": info})
}

func (s *Service) handleRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAdminErr(w, contract.ErrNotFound)
		return
	}
	if err := s.Revoke(r.Context(), p, id, r.URL.Query().Get("reason")); err != nil {
		writeAdminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LogEntry 는 동기화 기록 한 줄이다.
type LogEntry struct {
	ID         int64    `json:"id"`
	At         string   `json:"at"`
	Source     string   `json:"source"`
	TokenName  string   `json:"tokenName"`
	Method     string   `json:"method"`
	Resource   string   `json:"resource"`
	Op         string   `json:"op"`
	Result     string   `json:"result"`
	HTTPStatus int      `json:"httpStatus"`
	ScimType   string   `json:"scimType"`
	ExternalID string   `json:"externalId"`
	UserID     int64    `json:"userId"`
	Summary    string   `json:"summary"`
	Warnings   []string `json:"warnings"`
}

// ListLog 는 동기화 기록을 최신순으로 돌려준다(scim:read). before 는 이 번호보다 오래된 줄만, limit 은 1~200(기본 50).
func (s *Service) ListLog(ctx context.Context, p contract.Principal, result, source, from, to string, before int64, limit int) ([]LogEntry, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermSCIMRead); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	where, args := []string{"1 = 1"}, []any{}
	if result != "" {
		where, args = append(where, "l.result = ?"), append(args, result)
	}
	if source != "" {
		where, args = append(where, "l.source = ?"), append(args, source)
	}
	if from != "" {
		where, args = append(where, "l.at >= ?"), append(args, from)
	}
	if to != "" {
		where, args = append(where, "l.at < ?"), append(args, to)
	}
	if before > 0 {
		where, args = append(where, "l.id < ?"), append(args, before)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.at, l.source, COALESCE(t.name, ''), l.method, l.resource, l.op, l.result, l.http_status, l.scim_type, l.external_id, l.user_id, l.summary, l.warnings
		FROM scim_sync_log l LEFT JOIN scim_tokens t ON t.id = l.token_id WHERE `+strings.Join(where, " AND ")+` ORDER BY l.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		var uid sql.NullInt64
		var warn string
		if err := rows.Scan(&e.ID, &e.At, &e.Source, &e.TokenName, &e.Method, &e.Resource, &e.Op, &e.Result, &e.HTTPStatus, &e.ScimType, &e.ExternalID, &uid, &e.Summary, &warn); err != nil {
			return nil, err
		}
		e.UserID = uid.Int64
		_ = json.Unmarshal([]byte(warn), &e.Warnings)
		if e.Warnings == nil {
			e.Warnings = []string{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) handleLog(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := s.ListLog(r.Context(), p, q.Get("result"), q.Get("source"), q.Get("from"), q.Get("to"), before, limit)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
