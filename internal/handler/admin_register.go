// (internal/handler/admin*).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// RegisterAdmin 은 감사 로그 조회·체인 검증, 개인 토큰, 사용자·역할 관리 라우트를 등록한다.
// 호출: cmd/server/routes_platform.go. 모든 라우트는 r.HandleFunc 에 Permission 을 선언한다.
// 핸들러 안에서 역할을 비교하지 않는다(서비스가 Authorize 로 한 번 더 막는다).
func RegisterAdmin(r contract.Registrar, d deps.Deps) {
	h := &adminHandlers{d: d}
	r.HandleFunc("GET", "/api/audit-logs", contract.PermAuditRead, h.listAuditLogs)
	r.HandleFunc("POST", "/api/audit-logs/verify", contract.PermAuditVerify, h.verifyAuditChain)
	// 외부 앵커를 지금 한 번 기록한다(dev·e2e·운영 점검용). 주기 기록은 서버가 따로 한다. 검증 권한과 같다.
	r.HandleFunc("POST", "/api/audit-logs/anchor", contract.PermAuditVerify, h.stampAnchor)
	// 앵커 파일이 유실된 뒤 감사자가 명시적으로 앵커를 다시 시작한다(B9). 유실 상태가 아니면 409.
	r.HandleFunc("POST", "/api/audit-logs/anchor/restart", contract.PermAuditVerify, h.restartAnchor)
	r.HandleFunc("GET", "/api/tokens", contract.PermTokenManage, h.listTokens)
	// 토큰 발급·폐기는 로그인 세션에서만(개인 토큰으로 장기 토큰을 찍어 내지 못하게 한다).
	r.HandleFunc("POST", "/api/tokens", contract.PermTokenManage, rbac.SessionOnly(d.Audit, d.Txs, h.issueToken))
	r.HandleFunc("DELETE", "/api/tokens/{id}", contract.PermTokenManage, rbac.SessionOnly(d.Audit, d.Txs, h.revokeToken))
	r.HandleFunc("GET", "/api/admin/users", contract.PermUserManage, h.listUsers)
	r.HandleFunc("PATCH", "/api/admin/users/{id}", contract.PermUserManage, h.updateUser)
	// 관리자 설정(B2): 이미지 증빙의 LLM 전송 허용. 조회·변경 모두 admin(llm:policy_manage).
	r.HandleFunc("GET", "/api/admin/settings/llm-image", contract.PermLLMPolicyManage, h.getLLMImage)
	r.HandleFunc("PUT", "/api/admin/settings/llm-image", contract.PermLLMPolicyManage, h.setLLMImage)
}

type adminHandlers struct{ d deps.Deps }

func adminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// adminErr 는 계약 오류를 상태 코드로 옮긴다. 알 수 없는 오류는 내용을 숨기고 500 이다.
func adminErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		code = http.StatusUnauthorized
	case errors.Is(err, contract.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, contract.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, contract.ErrValidation):
		code = http.StatusBadRequest
	case errors.Is(err, contract.ErrInvalidState):
		code = http.StatusConflict
	}
	if code == http.StatusInternalServerError {
		log.Printf("admin: %v", err)
		if errors.Is(err, contract.ErrAuditFailed) {
			http.Error(w, contract.ErrAuditFailed.Error(), code)
			return
		}
		http.Error(w, "서버 오류가 발생했습니다", code)
		return
	}
	http.Error(w, err.Error(), code)
}

func adminPrincipal(w http.ResponseWriter, r *http.Request) (contract.Principal, bool) {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		adminErr(w, contract.ErrUnauthenticated)
	}
	return p, ok
}

func adminID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		adminErr(w, contract.ErrNotFound)
		return 0, false
	}
	return id, true
}

// ---- 감사 로그 ----

type recordJSON struct {
	Seq        int64            `json:"seq"`
	At         time.Time        `json:"at"`
	ActorID    int64            `json:"actorId"`
	ActorRole  string           `json:"actorRole"`
	Channel    string           `json:"channel"`
	Action     string           `json:"action"`
	TargetType string           `json:"targetType"`
	TargetID   string           `json:"targetId"`
	Result     string           `json:"result"`
	Diff       []audit.DiffItem `json:"diff"`
	Detail     string           `json:"detail"`
	PrevHash   string           `json:"prevHash"`
	Hash       string           `json:"hash"`
	// 표시용(해시 대상 아님, 조회 때 붙인다): 행위자 이름, diff 의 token_id 가 가리키는 토큰의 이름·앞자리(원문·해시 아님).
	ActorName  string `json:"actorName,omitempty"`
	TokenLabel string `json:"tokenLabel,omitempty"`
	// AI 도구(MCP) 경로 기록의 사용자 칸·상세에 보이는 개인 토큰 이름. 폐기된 토큰도 조회되고, 없으면 비어 있다(화면이 안내 문구를 쓴다).
	TokenName string `json:"tokenName,omitempty"`
}

// displayNames 는 감사 기록 화면용 이름을 조회한다(읽기만 한다). 사용자 이름과 토큰 이름·앞자리.
func (h *adminHandlers) displayNames(ctx context.Context, recs []audit.Record) (map[int64]string, map[int64]string, map[int64]string) {
	users, tokens, names := map[int64]string{}, map[int64]string{}, map[int64]string{}
	var uids, tids []int64
	seenU, seenT := map[int64]bool{}, map[int64]bool{}
	for _, rec := range recs {
		if rec.ActorID != 0 && !seenU[rec.ActorID] {
			seenU[rec.ActorID] = true
			uids = append(uids, rec.ActorID)
		}
		for _, d := range rec.Diff {
			if d.Key != audit.DiffKeyTokenID {
				continue
			}
			if id, err := strconv.ParseInt(d.New, 10, 64); err == nil && id > 0 && !seenT[id] {
				seenT[id] = true
				tids = append(tids, id)
			}
		}
	}
	if h.d.DB != nil && len(uids) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(uids)), ",")
		args := make([]any, len(uids))
		for i, id := range uids {
			args[i] = id
		}
		if rows, err := h.d.DB.QueryContext(ctx, `SELECT id, name FROM users WHERE id IN (`+marks+`)`, args...); err == nil {
			for rows.Next() {
				var id int64
				var name string
				if rows.Scan(&id, &name) == nil {
					users[id] = name
				}
			}
			rows.Close()
		}
		// 퇴사한 사람의 과거 기록은 그대로 보이되 이름에 "(퇴사)" 를 붙인다(명세 3.7).
		if osvc, ok := h.d.Org.(*org.Service); ok {
			if left, err := osvc.LeftUsers(ctx, uids); err == nil {
				for id := range left {
					if n, ok := users[id]; ok {
						users[id] = n + " (퇴사)"
					}
				}
			}
		}
	}
	if h.d.TokenLookup != nil && len(tids) > 0 {
		if infos, err := h.d.TokenLookup.TokenInfos(ctx, tids); err == nil {
			for id, ti := range infos {
				tokens[id] = fmt.Sprintf("%s (%s…)", ti.Name, ti.Prefix)
				names[id] = ti.Name
			}
		}
	}
	return users, tokens, names
}

func parseTimeParam(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// actorIDsByName 은 이름에 text 가 들어 있는 사용자 번호를 돌려준다(LIKE 와일드카드는 글자 그대로 취급).
func (h *adminHandlers) actorIDsByName(ctx context.Context, text string) ([]int64, error) {
	if h.d.DB == nil {
		return nil, errors.New("사용자 조회기가 구성되지 않았습니다")
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
	rows, err := h.d.DB.QueryContext(ctx, `SELECT id FROM users WHERE name LIKE ? ESCAPE '\' ORDER BY id LIMIT 200`, "%"+esc+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (h *adminHandlers) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.d.AuditReader == nil {
		adminErr(w, errors.New("감사 로그 조회기가 구성되지 않았습니다"))
		return
	}
	q := r.URL.Query()
	f := audit.Filter{
		Action: q.Get("action"), TargetType: audit.TargetType(q.Get("targetType")), TargetID: q.Get("targetId"),
		Result: audit.Result(q.Get("result")), Channel: contract.Channel(q.Get("channel")), Desc: true,
	}
	var err error
	if f.Entities, err = h.auditEntityScope(r.Context(), q); err != nil {
		adminErr(w, err)
		return
	}
	if v := q.Get("actorId"); v != "" {
		if f.ActorID, err = strconv.ParseInt(v, 10, 64); err != nil {
			adminErr(w, contract.ErrValidation)
			return
		}
	}
	if v := q.Get("beforeSeq"); v != "" {
		if f.AfterSeq, err = strconv.ParseInt(v, 10, 64); err != nil {
			adminErr(w, contract.ErrValidation)
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		if f.Limit, err = strconv.Atoi(v); err != nil {
			adminErr(w, contract.ErrValidation)
			return
		}
	}
	if f.From, err = parseTimeParam(q.Get("from")); err != nil {
		adminErr(w, contract.ErrValidation)
		return
	}
	if f.To, err = parseTimeParam(q.Get("to")); err != nil {
		adminErr(w, contract.ErrValidation)
		return
	}
	// actorName: 사용자 이름(일부)으로 거르는 화면용 매개변수. 맞는 사용자가 없으면 빈 목록이다.
	if name := strings.TrimSpace(q.Get("actorName")); name != "" {
		ids, err := h.actorIDsByName(r.Context(), name)
		if err != nil {
			adminErr(w, err)
			return
		}
		if len(ids) == 0 {
			adminJSON(w, http.StatusOK, map[string]any{"records": []recordJSON{}, "nextBeforeSeq": int64(0)})
			return
		}
		f.ActorIDs = ids
	}
	recs, err := h.d.AuditReader.List(r.Context(), f)
	if err != nil {
		adminErr(w, err)
		return
	}
	userNames, tokenLabels, tokenNames := h.displayNames(r.Context(), recs)
	out := make([]recordJSON, 0, len(recs))
	for _, rec := range recs {
		diff := rec.Diff
		if diff == nil {
			diff = []audit.DiffItem{}
		}
		j := recordJSON{Seq: rec.Seq, At: rec.At, ActorID: rec.ActorID, ActorRole: string(rec.ActorRole), Channel: string(rec.Channel),
			Action: rec.Action, TargetType: string(rec.TargetType), TargetID: rec.TargetID, Result: string(rec.Result), Diff: diff,
			Detail: rec.Detail, PrevHash: rec.PrevHash, Hash: rec.Hash, ActorName: userNames[rec.ActorID]}
		for _, d := range diff {
			if d.Key == audit.DiffKeyTokenID {
				if id, err := strconv.ParseInt(d.New, 10, 64); err == nil {
					j.TokenLabel = tokenLabels[id]
					j.TokenName = tokenNames[id]
				}
			}
		}
		out = append(out, j)
	}
	var next int64
	if len(out) > 0 && (f.Limit == 0 || len(out) >= f.Limit) {
		next = out[len(out)-1].Seq
	}
	adminJSON(w, http.StatusOK, map[string]any{"records": out, "nextBeforeSeq": next})
}

func (h *adminHandlers) verifyAuditChain(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.AuditReader == nil || h.d.Audit == nil || h.d.Txs == nil {
		adminErr(w, errors.New("감사 로그가 구성되지 않았습니다"))
		return
	}
	res, err := h.d.AuditReader.Verify(r.Context())
	if err != nil {
		adminErr(w, err)
		return
	}
	// 검증 실행 자체도 감사 대상이다. 변조가 발견되면 error 로 남긴다.
	result, detail := audit.ResultSuccess, ""
	if !res.OK {
		result, detail = audit.ResultError, "체인 불일치 seq "+strconv.FormatInt(res.BrokenSeq, 10)
		if res.Anchor.State == audit.AnchorLost {
			detail = "외부 앵커 유실"
		}
	}
	err = h.d.Txs.WithTx(r.Context(), func(tx contract.Tx) error {
		return h.d.Audit.Record(r.Context(), tx, audit.Entry{Actor: p, Action: "audit_log.verify",
			TargetType: audit.TargetAuditLog, TargetID: "chain", Result: result, Detail: detail})
	})
	if err != nil {
		adminErr(w, err)
		return
	}
	adminJSON(w, http.StatusOK, map[string]any{
		"ok": res.OK, "externallyVerified": res.ExternallyVerified(), "level": res.Level(), "summary": res.Summary(),
		"chainOk": res.ChainOK, "checked": res.Checked, "brokenSeq": res.BrokenSeq, "brokenWhy": res.BrokenWhy,
		"headHash": res.HeadHash, "verifiedAt": res.VerifiedAt, "anchor": anchorJSON(res.Anchor),
	})
}

func anchorJSON(a audit.AnchorStatus) map[string]any {
	state := a.State
	if state == "" { // 앵커 정보를 채우지 않는 구현은 꺼진 것으로 본다
		state = audit.AnchorDisabled
	}
	m := map[string]any{"state": state, "message": a.Message, "lines": a.Lines, "lastSeq": a.LastSeq, "lostHistory": a.LostHistory}
	if !a.LastAt.IsZero() {
		m["lastAt"] = a.LastAt
	}
	return m
}

// stampAnchor 는 체인 머리를 외부 앵커 파일에 지금 기록한다. 앵커가 꺼져 있으면 409 다.
// 앵커 기록 뒤에 감사 레코드를 남기므로, 그 레코드는 다음 앵커부터 포함된다.
func (h *adminHandlers) stampAnchor(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.AuditAnchor == nil {
		adminErr(w, fmt.Errorf("%w: 외부 앵커가 설정되지 않았습니다", contract.ErrInvalidState))
		return
	}
	if h.d.Audit == nil || h.d.Txs == nil {
		adminErr(w, errors.New("감사 로그가 구성되지 않았습니다"))
		return
	}
	res, err := h.d.AuditAnchor.Stamp(r.Context())
	if errors.Is(err, audit.ErrAnchorLost) {
		// 유실을 조용히 덮지 않는다: 별도 동작으로 감사 로그에 남기고 409 로 알린다.
		log.Printf("admin: 감사 앵커 유실: %v", err)
		if rerr := h.d.Txs.WithTx(r.Context(), func(tx contract.Tx) error {
			return h.d.Audit.Record(r.Context(), tx, audit.Entry{Actor: p, Action: "audit_log.anchor_lost",
				TargetType: audit.TargetAuditLog, TargetID: "anchor", Result: audit.ResultError, Detail: "외부 앵커 유실"})
		}); rerr != nil {
			adminErr(w, rerr)
			return
		}
		adminErr(w, fmt.Errorf("%w: 외부 앵커 기록이 유실되었습니다. 원인을 확인한 뒤 앵커를 다시 시작하세요", contract.ErrInvalidState))
		return
	}
	if err != nil {
		log.Printf("admin: 감사 앵커 기록: %v", err)
		http.Error(w, "외부 앵커를 기록하지 못했습니다. 서버 로그를 확인하세요", http.StatusInternalServerError)
		return
	}
	err = h.d.Txs.WithTx(r.Context(), func(tx contract.Tx) error {
		return h.d.Audit.Record(r.Context(), tx, audit.Entry{Actor: p, Action: "audit_log.anchor",
			TargetType: audit.TargetAuditLog, TargetID: "anchor", Result: audit.ResultSuccess})
	})
	if err != nil {
		adminErr(w, err)
		return
	}
	adminJSON(w, http.StatusOK, map[string]any{"written": res.Written, "seq": res.Seq, "at": res.At})
}

// restartAnchor 는 유실된 외부 앵커를 명시적으로 다시 시작한다. 재시작 행과 감사 레코드는 같은 트랜잭션이다.
// 재시작 뒤 새 앵커를 한 번 찍는다. 이후에도 검증 결과에는 "앵커 유실 이력 있음"이 남는다.
func (h *adminHandlers) restartAnchor(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	rs, _ := h.d.AuditAnchor.(audit.AnchorRestarter)
	if rs == nil {
		adminErr(w, fmt.Errorf("%w: 외부 앵커가 설정되지 않았습니다", contract.ErrInvalidState))
		return
	}
	if h.d.Audit == nil || h.d.Txs == nil {
		adminErr(w, errors.New("감사 로그가 구성되지 않았습니다"))
		return
	}
	err := h.d.Txs.WithTx(r.Context(), func(tx contract.Tx) error {
		if err := rs.Restart(r.Context(), tx); err != nil {
			return err
		}
		return h.d.Audit.Record(r.Context(), tx, audit.Entry{Actor: p, Action: "audit_log.anchor_restart",
			TargetType: audit.TargetAuditLog, TargetID: "anchor", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "anchor_state", Old: string(audit.AnchorLost), New: "restarted"}}})
	})
	if err != nil {
		adminErr(w, err)
		return
	}
	res, err := h.d.AuditAnchor.Stamp(r.Context())
	if err != nil {
		log.Printf("admin: 감사 앵커 재시작 뒤 기록: %v", err)
		http.Error(w, "앵커는 다시 시작했지만 첫 앵커를 기록하지 못했습니다. 서버 로그를 확인하세요", http.StatusInternalServerError)
		return
	}
	adminJSON(w, http.StatusOK, map[string]any{"restarted": true, "written": res.Written, "seq": res.Seq, "at": res.At})
}

// ---- 개인 토큰 ----

type tokenJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func toTokenJSON(t contract.TokenInfo) tokenJSON {
	return tokenJSON{t.ID, t.Name, t.Prefix, t.CreatedAt, t.ExpiresAt, t.RevokedAt, t.LastUsedAt}
}

func (h *adminHandlers) listTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.Tokens == nil {
		adminErr(w, errors.New("토큰 관리자가 구성되지 않았습니다"))
		return
	}
	list, err := h.d.Tokens.List(r.Context(), p)
	if err != nil {
		adminErr(w, err)
		return
	}
	out := make([]tokenJSON, 0, len(list))
	for _, t := range list {
		out = append(out, toTokenJSON(t))
	}
	adminJSON(w, http.StatusOK, out)
}

func (h *adminHandlers) issueToken(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.Tokens == nil {
		adminErr(w, errors.New("토큰 관리자가 구성되지 않았습니다"))
		return
	}
	var in struct {
		Name    string `json:"name"`
		TTLDays int    `json:"ttlDays"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.TTLDays < 0 || in.TTLDays > 3650 {
		adminErr(w, contract.ErrValidation)
		return
	}
	raw, info, err := h.d.Tokens.Issue(r.Context(), p, in.Name, time.Duration(in.TTLDays)*24*time.Hour)
	if err != nil {
		adminErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// 원문은 이 응답에서 한 번만 나간다.
	adminJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": toTokenJSON(info)})
}

func (h *adminHandlers) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	if h.d.Tokens == nil {
		adminErr(w, errors.New("토큰 관리자가 구성되지 않았습니다"))
		return
	}
	if err := h.d.Tokens.Revoke(r.Context(), p, id); err != nil {
		adminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- 사용자 관리 ----

type userJSON struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	Position       string `json:"position"`
	DepartmentID   int64  `json:"departmentId"`
	DepartmentName string `json:"departmentName"`
	Active         bool   `json:"active"`
}

func toUserJSON(u contract.UserInfo) userJSON {
	return userJSON{u.ID, u.Email, u.Name, string(u.Role), u.Position, u.DepartmentID, u.DepartmentName, u.Active}
}

func (h *adminHandlers) listUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.Users == nil {
		adminErr(w, errors.New("사용자 관리자가 구성되지 않았습니다"))
		return
	}
	list, err := h.d.Users.List(r.Context(), p)
	if err != nil {
		adminErr(w, err)
		return
	}
	// 다른 사람의 이메일 원문이 실리므로 응답을 만들기 전에 열람 기록을 남긴다(B41, 기록하지 못하면 목록을 내주지 않는다).
	if h.d.Privacy != nil {
		subs := make([]privacy.Subject, 0, len(list))
		for _, u := range list {
			if u.Email != "" {
				subs = append(subs, privacy.Subject{Type: privacy.SubjectUser, ID: u.ID})
			}
		}
		if err := h.d.Privacy.Note(r.Context(), p, privacy.Access{Category: privacy.CatContact, Screen: privacy.ScreenUserAdmin, Fields: []string{privacy.FieldEmail}, Subjects: privacy.ExcludeSelf(p, subs)}); err != nil {
			log.Printf("admin: 열람 기록 실패(사용자 관리 화면): %v", err)
			adminErr(w, err)
			return
		}
	}
	out := make([]userJSON, 0, len(list))
	for _, u := range list {
		out = append(out, toUserJSON(u))
	}
	adminJSON(w, http.StatusOK, out)
}

func (h *adminHandlers) updateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	if h.d.Users == nil {
		adminErr(w, errors.New("사용자 관리자가 구성되지 않았습니다"))
		return
	}
	var in struct {
		Role         *string `json:"role"`
		DepartmentID *int64  `json:"departmentId"` // 받으면 거부한다(소속은 인사 발령으로만 바꾼다)
		Active       *bool   `json:"active"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		adminErr(w, contract.ErrValidation)
		return
	}
	if in.DepartmentID != nil {
		http.Error(w, "부서는 인사 발령으로만 바꿀 수 있습니다.", http.StatusBadRequest)
		return
	}
	patch := contract.UserPatch{Active: in.Active}
	if in.Role != nil {
		role := contract.Role(*in.Role)
		patch.Role = &role
	}
	u, err := h.d.Users.Update(r.Context(), p, id, patch)
	if err != nil {
		adminErr(w, err)
		return
	}
	adminJSON(w, http.StatusOK, toUserJSON(u))
}

func (h *adminHandlers) getLLMImage(w http.ResponseWriter, r *http.Request) {
	if h.d.Settings == nil {
		adminErr(w, errors.New("설정 저장소가 구성되지 않았습니다"))
		return
	}
	adminJSON(w, http.StatusOK, map[string]bool{"enabled": h.d.Settings.LLMImageTransmission(r.Context())})
}

func (h *adminHandlers) setLLMImage(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	if h.d.Settings == nil {
		adminErr(w, errors.New("설정 저장소가 구성되지 않았습니다"))
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.Enabled == nil {
		adminErr(w, contract.ErrValidation)
		return
	}
	if err := h.d.Settings.SetLLMImageTransmission(r.Context(), p, *in.Enabled); err != nil {
		adminErr(w, err)
		return
	}
	adminJSON(w, http.StatusOK, map[string]bool{"enabled": *in.Enabled})
}
