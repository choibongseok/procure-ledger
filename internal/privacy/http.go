package privacy

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

// Register 는 조회 기록 화면 API 를 선언과 함께 등록한다. 쓰기·수정·삭제 라우트는 없다(기록은 Recorder 한 곳만 쓴다).
func (r *Recorder) Register(reg contract.Registrar) {
	reg.HandleFunc("GET", "/api/admin/privacy/log", contract.PermPrivacyLogRead, r.hList)
	reg.HandleFunc("GET", "/api/admin/privacy/log/{id}/subjects", contract.PermPrivacyLogRead, r.hSubjects)
	reg.HandleFunc("GET", "/api/admin/privacy/stats", contract.PermPrivacyLogRead, r.hStats)
	reg.HandleFunc("POST", "/api/admin/privacy/verify", contract.PermPrivacyLogRead, r.hVerify)
	reg.HandleFunc("GET", "/api/admin/privacy/retention", contract.PermPrivacyLogManage, r.hRetention)
	reg.HandleFunc("POST", "/api/privacy/requests", contract.PermAuthenticated, r.hCreateRequest)
	reg.HandleFunc("GET", "/api/privacy/requests", contract.PermAuthenticated, r.hMyRequests)
	reg.HandleFunc("GET", "/api/privacy/requests/{id}/result", contract.PermAuthenticated, r.hMyResult)
	// 열람 요청 처리: 서비스가 privacy:request_manage 또는 감사 대행(D9)을 요청마다 판정한다(선언은 로그인만)
	reg.HandleFunc("GET", "/api/admin/privacy/requests", contract.PermAuthenticated, r.hListRequests)
	reg.HandleFunc("POST", "/api/admin/privacy/requests/{id}/approve", contract.PermAuthenticated, rbac.SessionOnly(r.audit, r.txs, r.hDecide(true)))
	reg.HandleFunc("POST", "/api/admin/privacy/requests/{id}/reject", contract.PermAuthenticated, rbac.SessionOnly(r.audit, r.txs, r.hDecide(false)))
	reg.HandleFunc("PUT", "/api/admin/privacy/retention", contract.PermPrivacyLogManage, rbac.SessionOnly(r.audit, r.txs, r.hSetRetention))
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "찾을 수 없습니다"})
	case errors.Is(err, contract.ErrInvalidState):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, contract.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": ")})
	default:
		log.Printf("privacy: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요."})
	}
}

func principal(w http.ResponseWriter, req *http.Request) (contract.Principal, bool) {
	p, ok := contract.PrincipalFrom(req.Context())
	if !ok {
		writeErr(w, contract.ErrUnauthenticated)
	}
	return p, ok
}

// parseTimeParam 은 RFC3339 또는 날짜(YYYY-MM-DD, 한국 시간 하루의 시작)를 읽는다. 빈 값은 영 시각이다.
func parseTimeParam(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", s, kst)
}

func (r *Recorder) hList(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	q := req.URL.Query()
	var f Filter
	var err error
	if f.From, err = parseTimeParam(q.Get("from")); err != nil {
		writeErr(w, verr("시작 시각 형식이 올바르지 않습니다."))
		return
	}
	if f.To, err = parseTimeParam(q.Get("to")); err != nil {
		writeErr(w, verr("종료 시각 형식이 올바르지 않습니다."))
		return
	}
	f.ActorID, _ = strconv.ParseInt(q.Get("actorId"), 10, 64)
	f.ActorName, f.Category, f.Screen, f.SubjectName = q.Get("actor"), q.Get("category"), q.Get("screen"), q.Get("subject")
	f.ExportOnly, f.AnomalyOnly = q.Get("export") == "1", q.Get("anomaly") == "1"
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	items, err := r.List(req.Context(), p, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	days, _ := r.RetentionDays(req.Context())
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "retentionDays": days})
}

func (r *Recorder) hSubjects(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, contract.ErrNotFound)
		return
	}
	offset, _ := strconv.Atoi(req.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	items, total, err := r.Subjects(req.Context(), p, id, offset, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (r *Recorder) hStats(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(req.URL.Query().Get("days"))
	st, err := r.Stats(req.Context(), p, days)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (r *Recorder) hVerify(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	if err := r.authorizeRead(req.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
	res, err := r.Verify(req.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := r.recordRead(req.Context(), p, "verify", []string{"무결성 확인"}, int(res.Checked)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (r *Recorder) hRetention(w http.ResponseWriter, req *http.Request) {
	days, err := r.RetentionDays(req.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	pending, err := r.PendingPurge(req.Context(), r.now())
	if err != nil {
		writeErr(w, err)
		return
	}
	if pending == nil {
		pending = []PurgeItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionDays": days, "min": MinRetentionDays, "max": MaxRetentionDays, "defaultDays": DefaultRetentionDays, "pending": pending})
}

func (r *Recorder) hSetRetention(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	var in struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return
	}
	if err := r.SetRetention(req.Context(), p, in.Days); err != nil {
		writeErr(w, err)
		return
	}
	r.hRetention(w, req)
}

func (r *Recorder) hCreateRequest(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	var body struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<14)).Decode(&body); err != nil {
		writeErr(w, verr("요청 형식이 올바르지 않습니다"))
		return
	}
	id, err := r.CreateRequest(req.Context(), p, body.From, body.To, body.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (r *Recorder) hMyRequests(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	out, err := r.MyRequests(req.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Recorder) hMyResult(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
	out, err := r.MyResult(req.Context(), p, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// denyAudit 는 서비스가 열람 요청 처리를 거부했을 때 route 계층(rbac)과 같은 형태의 거부 감사 access.denied 를 남긴다.
// 이 라우트들은 선언 권한이 로그인뿐이고 처리 자격(privacy:request_manage 또는 감사 대행)을 서비스가 요청마다 판정하기 때문이다.
func (r *Recorder) denyAudit(req *http.Request, p contract.Principal, err error) {
	if !errors.Is(err, contract.ErrForbidden) {
		return
	}
	target := req.Pattern
	if target == "" {
		target = req.Method + " " + req.URL.Path
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 5*time.Second)
	defer cancel()
	if aerr := r.txs.WithTx(ctx, func(tx contract.Tx) error {
		return r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "access.denied", TargetType: audit.TargetRoute, TargetID: target, Result: audit.ResultDenied,
			Detail: "필요 권한 " + string(contract.PermPrivacyRequestManage) + " 또는 감사 대행(" + string(contract.PermPrivacyLogRead) + ", 요청자가 유일한 관리자일 때)"})
	}); aerr != nil {
		log.Printf("privacy: 거부 기록 실패: %v", aerr)
	}
}

func (r *Recorder) hListRequests(w http.ResponseWriter, req *http.Request) {
	p, ok := principal(w, req)
	if !ok {
		return
	}
	out, err := r.ListRequests(req.Context(), p)
	if err != nil {
		r.denyAudit(req, p, err)
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Recorder) hDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		p, ok := principal(w, req)
		if !ok {
			return
		}
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		var body struct {
			Note string `json:"note"`
		}
		if req.ContentLength != 0 {
			if err := json.NewDecoder(io.LimitReader(req.Body, 1<<14)).Decode(&body); err != nil {
				writeErr(w, verr("요청 형식이 올바르지 않습니다"))
				return
			}
		}
		if err := r.DecideRequest(req.Context(), p, id, approve, body.Note); err != nil {
			r.denyAudit(req, p, err)
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
