package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// SCIM Groups(B40 3단계, 명세 9절). 그룹 연동 방식은 설정 하나로 정한다: unit(그룹=조직, 멤버가 그 조직으로 전보) 또는 role(그룹=역할 부여).
// 그룹과 대상의 연결(매핑)은 관리자 화면에서만 만든다. IdP 가 그룹 이름으로 역할을 고를 수 없게 해 위험 권한이 새는 길을 막는다.

type groupMap struct {
	ID       int64
	Source   string
	Ext      string
	Display  string
	Kind     string
	UnitID   sql.NullInt64
	RoleID   sql.NullInt64
	Members  int
	UnitCode string
	// Scope 는 역할 모드 부여의 범위다(own | entity, B44).
	Scope         string
	ScopeEntityID int64
}

func (s *Service) scanMap(row interface{ Scan(...any) error }) (groupMap, error) {
	var m groupMap
	err := row.Scan(&m.ID, &m.Source, &m.Ext, &m.Display, &m.Kind, &m.UnitID, &m.RoleID, &m.Scope, &m.ScopeEntityID)
	return m, err
}

const mapCols = `m.id, m.source, m.group_external_id, m.display_name, m.kind, m.unit_id, m.role_id, m.scope, m.scope_entity_id`

func (s *Service) loadMap(ctx context.Context, q querier, source string, id int64) (groupMap, error) {
	m, err := s.scanMap(q.QueryRowContext(ctx, `SELECT `+mapCols+` FROM scim_group_maps m WHERE m.source = ? AND m.id = ?`, source, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, errNotFound("그룹을 찾을 수 없습니다.")
	}
	return m, err
}

func (s *Service) mapByExt(ctx context.Context, q querier, source, ext string) (groupMap, bool, error) {
	m, err := s.scanMap(q.QueryRowContext(ctx, `SELECT `+mapCols+` FROM scim_group_maps m WHERE m.source = ? AND m.group_external_id = ?`, source, ext))
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

type member struct {
	UID   int64
	Grant sql.NullInt64
}

func (s *Service) members(ctx context.Context, q querier, mapID int64) ([]member, error) {
	rows, err := q.QueryContext(ctx, `SELECT user_id, grant_id FROM scim_group_members WHERE map_id = ? ORDER BY user_id`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.UID, &m.Grant); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) groupResource(ctx context.Context, q querier, m groupMap) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT u.id, u.name FROM scim_group_members gm JOIN users u ON u.id = gm.user_id WHERE gm.map_id = ? ORDER BY u.id`, m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mem := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		mem = append(mem, map[string]any{"value": itoa(id), "display": name, "$ref": s.location("Users", id)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"schemas": []string{schemaGroup}, "id": itoa(m.ID), "externalId": m.Ext, "displayName": m.Display, "members": mem,
		"meta": map[string]any{"resourceType": "Group", "location": s.location("Groups", m.ID)},
	}, nil
}

// ---- 조회 ----

func (s *Service) listGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	q := r.URL.Query()
	empty := func() {
		writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{schemaList}, "totalResults": 0, "startIndex": 1, "itemsPerPage": 0, "Resources": []any{}})
	}
	if rc.st.GroupMode == "off" {
		empty()
		return
	}
	conds, fargs, ferr := parseGroupFilter(q.Get("filter"))
	if ferr != nil {
		writeAPIError(w, ferr)
		return
	}
	start := parseInt(q.Get("startIndex"), 1)
	if start < 1 {
		start = 1
	}
	count := parseInt(q.Get("count"), defaultCount)
	if count < 0 {
		count = 0
	}
	if count > maxCount {
		count = maxCount
	}
	where, args := "m.source = ?", []any{rc.tok.Source}
	for _, c := range conds {
		where += " AND " + c
	}
	args = append(args, fargs...)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_group_maps m WHERE `+where, args...).Scan(&total); err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	res := []map[string]any{}
	if count > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT `+mapCols+` FROM scim_group_maps m WHERE `+where+` ORDER BY m.id LIMIT ? OFFSET ?`, append(args, count, start-1)...)
		if err != nil {
			writeAPIError(w, toAPIError(err))
			return
		}
		var maps []groupMap
		for rows.Next() {
			m, err := s.scanMap(rows)
			if err != nil {
				rows.Close()
				writeAPIError(w, toAPIError(err))
				return
			}
			maps = append(maps, m)
		}
		rows.Close()
		for _, m := range maps {
			g, err := s.groupResource(ctx, s.db, m)
			if err != nil {
				writeAPIError(w, toAPIError(err))
				return
			}
			res = append(res, project(g, q.Get("attributes"), q.Get("excludedAttributes")))
		}
	}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{schemaList}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res})
}

// parseGroupFilter 는 그룹 필터(displayName eq·externalId eq, and 연결)를 SQL 조건으로 바꾼다.
func parseGroupFilter(f string) ([]string, []any, *apiError) {
	f = strings.TrimSpace(f)
	if f == "" {
		return nil, nil, nil
	}
	if len(f) > maxFilterLen {
		return nil, nil, errFilter("필터가 너무 깁니다.")
	}
	lf := strings.ToLower(f)
	if strings.Contains(lf, " or ") || strings.HasPrefix(lf, "not ") {
		return nil, nil, errFilter("or·not 은 지원하지 않습니다. and 로만 연결할 수 있습니다.")
	}
	var conds []string
	var args []any
	for _, part := range splitAnd(f) {
		cl, e := parseClause(part)
		if e != nil {
			return nil, nil, e
		}
		switch strings.ToLower(cl.attr) {
		case "displayname":
			if cl.op != "eq" {
				return nil, nil, errFilter("그룹의 displayName 은 eq 만 지원합니다.")
			}
			conds, args = append(conds, "lower(m.display_name) = ?"), append(args, strings.ToLower(cl.val))
		case "externalid":
			conds, args = append(conds, "m.group_external_id = ?"), append(args, cl.val)
		default:
			return nil, nil, errFilter("그룹은 displayName·externalId 필터만 지원합니다.")
		}
	}
	return conds, args, nil
}

func (s *Service) getGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if perr != nil || rc.st.GroupMode == "off" {
		writeAPIError(w, errNotFound("그룹을 찾을 수 없습니다."))
		return
	}
	m, err := s.loadMap(ctx, s.db, rc.tok.Source, id)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	g, err := s.groupResource(ctx, s.db, m)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	writeSCIM(w, http.StatusOK, project(g, r.URL.Query().Get("attributes"), r.URL.Query().Get("excludedAttributes")))
}

// ---- 쓰기 ----

type groupResult struct {
	MapID    int64
	Status   int
	Op       string
	Ext      string
	Display  string
	Added    int
	Removed  int
	Warnings []string
}

func (s *Service) runGroupWrite(w http.ResponseWriter, r *http.Request, method, opName string, op func(ctx context.Context, tx contract.Tx, rc *reqCtx) (groupResult, error)) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	if rc.st.GroupMode == "off" {
		writeAPIError(w, &apiError{Status: 403, Detail: "그룹 연동이 꺼져 있습니다. 관리자 화면에서 그룹 연동 방식(조직 또는 역할)을 고르세요."})
		return
	}
	var res groupResult
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var e error
		res, e = op(ctx, tx, rc)
		return e
	})
	if err != nil {
		ae := toAPIError(err)
		op := opName
		if ae.Reason == "no_map" {
			op = "rejected"
		}
		s.logSync(ctx, rc.tok, syncEntry{Method: method, Resource: "Group", Op: op, Status: ae.Status, ScimType: ae.Type, Ext: res.Ext, Summary: ae.Detail})
		if auditedReasons[ae.Reason] {
			s.auditThrottled(ctx, "rej|"+strconv.FormatInt(rc.tok.ID, 10)+"|"+ae.Reason, time.Minute, rc.actor, "scim.rejected", audit.TargetRoute, "/scim/v2/Groups", audit.ResultDenied,
				[]audit.DiffItem{{Key: "reason", New: ae.Reason}, {Key: "scim_type", New: ae.Type}, {Key: "token", New: rc.tok.Name}}, ae.Detail)
		}
		writeAPIError(w, ae)
		return
	}
	if res.Added+res.Removed > 0 || len(res.Warnings) > 0 {
		op := res.Op
		if op == "" {
			op = "group_set"
		}
		s.logSync(ctx, rc.tok, syncEntry{Method: method, Resource: "Group", Op: op, Status: res.Status, Ext: res.Ext, Warnings: res.Warnings,
			Summary: "그룹 " + res.Display + ": 추가 " + strconv.Itoa(res.Added) + "·제거 " + strconv.Itoa(res.Removed)})
	}
	if res.Status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	m, err := s.loadMap(ctx, s.db, rc.tok.Source, res.MapID)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	g, err := s.groupResource(ctx, s.db, m)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	if res.Status == http.StatusCreated {
		w.Header().Set("Location", s.location("Groups", m.ID))
	}
	writeSCIM(w, res.Status, g)
}

type groupIn struct {
	Schemas     []string        `json:"schemas"`
	ExternalID  *string         `json:"externalId"`
	DisplayName *string         `json:"displayName"`
	Members     json.RawMessage `json:"members"`
}

func decodeGroup(r *http.Request) (*groupIn, *apiError) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &apiError{Status: 413, Type: "tooMany", Detail: "요청 본문이 너무 큽니다(1MiB 이하)."}
		}
		return nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "요청 본문을 읽을 수 없습니다."}
	}
	in := &groupIn{}
	if err := json.Unmarshal(b, in); err != nil {
		return nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "JSON 형식이 올바르지 않습니다."}
	}
	return in, nil
}

// memberIDs 는 members 값(객체 배열 또는 객체 하나)에서 사용자 번호를 읽는다.
func memberIDs(raw json.RawMessage) ([]int64, *apiError) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		var one struct {
			Value string `json:"value"`
		}
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, errInvalidValue("members 는 {value} 객체의 배열이어야 합니다.")
		}
		items = append(items, one)
	}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		id, err := strconv.ParseInt(strings.TrimSpace(it.Value), 10, 64)
		if err != nil {
			return nil, errInvalidValue("멤버의 value 는 사용자 id 여야 합니다: " + it.Value)
		}
		out = append(out, id)
	}
	return out, nil
}

// applyMembership 은 그룹의 멤버 집합을 target 으로 맞춘다(추가·제거 차이만 처리). 한 트랜잭션이라 하나라도 거부되면 전부 취소된다.
func (s *Service) applyMembership(ctx context.Context, tx contract.Tx, rc *reqCtx, m groupMap, target []int64) (added, removed int, warns []string, err error) {
	if rc.st.GroupMode != m.Kind {
		return 0, 0, nil, errConflict("그룹 연동 방식이 이 그룹의 매핑과 다릅니다. 관리자 화면에서 매핑을 정리하세요.", "")
	}
	if err := dbutil.AcquireKeyLock(ctx, tx, "scim:group:"+itoa(m.ID)); err != nil {
		return 0, 0, nil, err
	}
	cur, err := s.members(ctx, tx, m.ID)
	if err != nil {
		return 0, 0, nil, err
	}
	have := map[int64]member{}
	for _, c := range cur {
		have[c.UID] = c
	}
	want := map[int64]bool{}
	for _, id := range target {
		want[id] = true
	}
	var add, rem []int64
	for id := range want {
		if _, ok := have[id]; !ok {
			add = append(add, id)
		}
	}
	for id := range have {
		if !want[id] {
			rem = append(rem, id)
		}
	}
	sort.Slice(add, func(i, j int) bool { return add[i] < add[j] })
	sort.Slice(rem, func(i, j int) bool { return rem[i] < rem[j] })
	// 추가할 사람은 먼저 모두 확인한다: 이 출처로 연결된 사용자이고 보호 계정이 아니어야 한다.
	for _, id := range add {
		if here, err := s.linkedHere(ctx, tx, rc.tok.Source, id); err != nil {
			return 0, 0, nil, err
		} else if !here {
			return 0, 0, nil, errInvalidValue("멤버 " + itoa(id) + " 은(는) 이 연동으로 동기화된 사용자가 아닙니다. 사용자를 먼저 동기화하세요.")
		}
		if p, err := s.protected(ctx, tx, id); err != nil {
			return 0, 0, nil, err
		} else if p {
			return 0, 0, nil, errMutability("위험 권한이 있는 계정은 그룹 멤버로 넣을 수 없습니다.", reasonProt)
		}
	}
	if m.Kind == "role" && len(add) > 0 {
		if risky, err := access.RoleRisk(ctx, tx, m.RoleID.Int64); err != nil {
			return 0, 0, nil, errInvalidValue("매핑된 역할을 찾을 수 없습니다. 관리자 화면에서 매핑을 정리하세요.")
		} else if len(risky) > 0 {
			return 0, 0, nil, errMutability("위험 권한이 든 역할은 SCIM 으로 줄 수 없습니다. 관리자 화면에서 매핑을 정리하세요.", reasonRisk)
		}
	}
	now := s.stamp()
	for _, id := range rem {
		c := have[id]
		if m.Kind == "role" && c.Grant.Valid {
			// 같은 부여를 다른 그룹 멤버 행도 가리키면(같은 역할이 매핑된 그룹이 둘 이상) 마지막 참조일 때만 회수한다.
			var others int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_group_members WHERE grant_id = ? AND NOT (map_id = ? AND user_id = ?)`, c.Grant.Int64, m.ID, id).Scan(&others); err != nil {
				return 0, 0, nil, err
			}
			if others == 0 {
				if err := s.access.SCIMRevoke(ctx, tx, rc.actor, c.Grant.Int64); err != nil {
					return 0, 0, nil, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_members WHERE map_id = ? AND user_id = ?`, m.ID, id); err != nil {
			return 0, 0, nil, err
		}
		removed++
	}
	var transfers []org.Command
	for _, id := range add {
		var grant any
		if m.Kind == "role" {
			gid, created, err := s.access.SCIMGrant(ctx, tx, rc.actor, m.RoleID.Int64, id, "SCIM 그룹 "+m.Display, m.Scope, m.ScopeEntityID)
			if err != nil {
				return 0, 0, nil, err
			}
			if created {
				grant = gid
			} else {
				// 이미 있는 부여가 다른 그룹 멤버 행이 만든 SCIM 부여라면 같은 번호를 함께 가리킨다(마지막 참조가 사라질 때 회수). 사람이 준 부여는 추적하지 않는다.
				var refs int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_group_members WHERE grant_id = ?`, gid).Scan(&refs); err != nil {
					return 0, 0, nil, err
				}
				if refs > 0 {
					grant = gid
				}
			}
		} else {
			v, err := s.loadView(ctx, tx, id, rc.tok.Source, rc.today)
			if err != nil {
				return 0, 0, nil, err
			}
			switch {
			case !v.State.Employed:
				warns = append(warns, "퇴사 상태인 사용자는 그룹 소속만 기록하고 전보는 재입사 때 반영합니다")
			case v.State.UnitID != m.UnitID.Int64:
				if n := s.headOf(ctx, tx, id, rc.today); n > 0 {
					warns = append(warns, "조직장 공석: 전보로 조직장 자리가 비었습니다")
				}
				if n := pendingApprovals(ctx, tx, id); n > 0 {
					warns = append(warns, "진행 중 결재 "+itoa(int64(n))+"건이 이 사람의 결재 차례입니다(전보 뒤 결재선이 달라질 수 있습니다)")
				}
				transfers = append(transfers, org.Command{Kind: org.KindTransfer, UserID: id, ToUnit: m.UnitID.Int64, Reason: s.reasonText(rc.tok.Source + " 그룹 전보")})
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO scim_group_members (map_id, user_id, grant_id, added_at) VALUES (?,?,?,?)`, m.ID, id, grant, now); err != nil {
			return 0, 0, nil, err
		}
		added++
	}
	if len(transfers) > 0 {
		outs, err := s.org.RegisterBatch(ctx, tx, rc.actor, transfers, true)
		if err != nil {
			return 0, 0, nil, err
		}
		for _, o := range outs {
			warns = append(warns, o.Warnings...)
		}
	}
	return added, removed, warns, nil
}

func ids(cur []member) []int64 {
	out := make([]int64, 0, len(cur))
	for _, c := range cur {
		out = append(out, c.UID)
	}
	return out
}

func (s *Service) setDisplay(ctx context.Context, tx contract.Tx, m *groupMap, name *string) error {
	if name == nil || strings.TrimSpace(*name) == "" || strings.TrimSpace(*name) == m.Display {
		return nil
	}
	m.Display = truncate(strings.TrimSpace(*name), 200)
	_, err := tx.ExecContext(ctx, `UPDATE scim_group_maps SET display_name = ? WHERE id = ?`, m.Display, m.ID)
	return err
}

// createGroup 은 POST /Groups 다. 매핑이 이미 있을 때만 성공하고(멤버는 본문의 목록으로 맞춘다), 매핑이 없으면 400 으로 거부하고 기록한다.
func (s *Service) createGroup(w http.ResponseWriter, r *http.Request) {
	in, ae := decodeGroup(r)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	s.runGroupWrite(w, r, http.MethodPost, "group_set", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (groupResult, error) {
		ext := str(in.ExternalID)
		if ext == "" {
			ext = str(in.DisplayName)
		}
		res := groupResult{Ext: ext, Display: str(in.DisplayName)}
		if ext == "" {
			return res, errInvalidValue("그룹의 externalId 또는 displayName 이 필요합니다.")
		}
		m, ok, err := s.mapByExt(ctx, tx, rc.tok.Source, ext)
		if err != nil {
			return res, err
		}
		if !ok {
			return res, &apiError{Status: 400, Type: "invalidValue", Detail: "매핑이 없는 그룹입니다. 관리자 화면에서 이 그룹을 조직 또는 역할에 연결하세요.", Reason: "no_map"}
		}
		tgt, ae := memberIDs(in.Members)
		if ae != nil {
			return res, ae
		}
		if err := s.setDisplay(ctx, tx, &m, in.DisplayName); err != nil {
			return res, err
		}
		res.MapID, res.Display, res.Status = m.ID, m.Display, http.StatusCreated
		res.Added, res.Removed, res.Warnings, err = s.applyMembership(ctx, tx, rc, m, tgt)
		return res, err
	})
}

func (s *Service) replaceGroup(w http.ResponseWriter, r *http.Request) {
	in, ae := decodeGroup(r)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	s.runGroupWrite(w, r, http.MethodPut, "group_set", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (groupResult, error) {
		m, err := s.targetMap(ctx, tx, rc, r.PathValue("id"))
		if err != nil {
			return groupResult{}, err
		}
		res := groupResult{MapID: m.ID, Ext: m.Ext, Display: m.Display, Status: http.StatusOK}
		tgt, ae := memberIDs(in.Members)
		if ae != nil {
			return res, ae
		}
		if err := s.setDisplay(ctx, tx, &m, in.DisplayName); err != nil {
			return res, err
		}
		res.Display = m.Display
		res.Added, res.Removed, res.Warnings, err = s.applyMembership(ctx, tx, rc, m, tgt)
		return res, err
	})
}

func (s *Service) deleteGroup(w http.ResponseWriter, r *http.Request) {
	s.runGroupWrite(w, r, http.MethodDelete, "group_remove", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (groupResult, error) {
		m, err := s.targetMap(ctx, tx, rc, r.PathValue("id"))
		if err != nil {
			return groupResult{}, err
		}
		res := groupResult{MapID: m.ID, Ext: m.Ext, Display: m.Display, Status: http.StatusNoContent, Op: "group_remove"}
		// 그룹 삭제는 멤버 전원 제거다(매핑은 남긴다). 같은 그룹이 다시 오면 멤버가 다시 채워진다.
		res.Added, res.Removed, res.Warnings, err = s.applyMembership(ctx, tx, rc, m, nil)
		return res, err
	})
}

func (s *Service) targetMap(ctx context.Context, tx contract.Tx, rc *reqCtx, idText string) (groupMap, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return groupMap{}, errNotFound("그룹을 찾을 수 없습니다.")
	}
	return s.loadMap(ctx, tx, rc.tok.Source, id)
}

var memberPath = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"?(\d+)"?\s*\]$`)

func (s *Service) patchGroup(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeAPIError(w, &apiError{Status: 413, Type: "tooMany", Detail: "요청 본문이 너무 큽니다(1MiB 이하)."})
			return
		}
		writeAPIError(w, &apiError{Status: 400, Type: "invalidSyntax", Detail: "요청 본문을 읽을 수 없습니다."})
		return
	}
	var body patchBody
	if err := json.Unmarshal(b, &body); err != nil || len(body.Operations) == 0 {
		writeAPIError(w, &apiError{Status: 400, Type: "invalidSyntax", Detail: "PatchOp 형식이 올바르지 않습니다(Operations 필요)."})
		return
	}
	s.runGroupWrite(w, r, http.MethodPatch, "group_set", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (groupResult, error) {
		m, err := s.targetMap(ctx, tx, rc, r.PathValue("id"))
		if err != nil {
			return groupResult{}, err
		}
		res := groupResult{MapID: m.ID, Ext: m.Ext, Display: m.Display, Status: http.StatusOK}
		cur, err := s.members(ctx, tx, m.ID)
		if err != nil {
			return res, err
		}
		set := map[int64]bool{}
		for _, id := range ids(cur) {
			set[id] = true
		}
		var newName *string
		for _, op := range body.Operations {
			verb := strings.ToLower(strings.TrimSpace(op.Op))
			path := strings.TrimSpace(op.Path)
			low := strings.ToLower(path)
			switch {
			case low == "displayname" && (verb == "replace" || verb == "add"):
				var v string
				if err := json.Unmarshal(op.Value, &v); err != nil {
					return res, errInvalidValue("displayName 은 문자열이어야 합니다.")
				}
				newName = &v
			case low == "members" || (low == "" && verb != "remove"):
				val := op.Value
				if low == "" { // 경로 없는 add·replace: 값이 {members:[...]} 객체다
					var obj struct {
						Members json.RawMessage `json:"members"`
					}
					if err := json.Unmarshal(val, &obj); err != nil || len(obj.Members) == 0 {
						return res, &apiError{Status: 400, Type: "invalidPath", Detail: "경로 없는 연산은 members 만 지원합니다."}
					}
					val = obj.Members
				}
				idsV, ae := memberIDs(val)
				if ae != nil {
					return res, ae
				}
				switch verb {
				case "add":
					for _, id := range idsV {
						set[id] = true
					}
				case "replace":
					set = map[int64]bool{}
					for _, id := range idsV {
						set[id] = true
					}
				case "remove":
					if len(idsV) == 0 {
						set = map[int64]bool{} // path=members 에 value 없는 remove 는 전체 제거
					}
					for _, id := range idsV {
						delete(set, id)
					}
				default:
					return res, &apiError{Status: 400, Type: "invalidSyntax", Detail: "지원하지 않는 연산입니다: " + op.Op}
				}
			case verb == "remove" && memberPath.MatchString(path):
				id, _ := strconv.ParseInt(memberPath.FindStringSubmatch(path)[1], 10, 64)
				delete(set, id)
			default:
				return res, &apiError{Status: 400, Type: "invalidPath", Detail: "지원하지 않는 경로입니다: " + path}
			}
		}
		if err := s.setDisplay(ctx, tx, &m, newName); err != nil {
			return res, err
		}
		res.Display = m.Display
		tgt := make([]int64, 0, len(set))
		for id := range set {
			tgt = append(tgt, id)
		}
		res.Added, res.Removed, res.Warnings, err = s.applyMembership(ctx, tx, rc, m, tgt)
		return res, err
	})
}

// memberUnitRef 는 조직 모드에서 사용자의 주 소속으로 쓸 조직 코드다(가장 최근에 들어온 조직 그룹). 없으면 "".
func (s *Service) memberUnitRef(ctx context.Context, q querier, source string, uid int64) string {
	var code string
	err := q.QueryRowContext(ctx, `SELECT u.code FROM scim_group_members gm JOIN scim_group_maps m ON m.id = gm.map_id JOIN org_units u ON u.id = m.unit_id
		WHERE gm.user_id = ? AND m.kind = 'unit' AND m.source = ? ORDER BY gm.added_at DESC, m.id DESC LIMIT 1`, uid, source).Scan(&code)
	if err != nil {
		return ""
	}
	return code
}

// userGroups 는 사용자가 속한 그룹(이 출처의 매핑)이다. Users 응답의 읽기 전용 groups 에 쓴다.
func (s *Service) userGroups(ctx context.Context, q querier, source string, uid int64) []map[string]any {
	rows, err := q.QueryContext(ctx, `SELECT m.id, m.display_name FROM scim_group_members gm JOIN scim_group_maps m ON m.id = gm.map_id WHERE gm.user_id = ? AND m.source = ? ORDER BY m.id`, uid, source)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			out = append(out, map[string]any{"value": itoa(id), "display": name, "$ref": s.location("Groups", id)})
		}
	}
	return out
}
