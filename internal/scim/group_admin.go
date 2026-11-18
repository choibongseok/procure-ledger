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
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// 그룹 매핑 관리(명세 9절, 화면 12절). 매핑은 관리자(scim:manage, 세션 전용)만 만든다. 위험 권한이 든 역할은 저장 단계에서 거부한다.

func (s *Service) registerGroupAdmin(r contract.Registrar) {
	r.HandleFunc("GET", "/api/admin/scim/group-maps", contract.PermSCIMRead, s.handleListMaps)
	r.HandleFunc("POST", "/api/admin/scim/group-maps", contract.PermSCIMManage, rbac.SessionOnly(s.audit, s.txs, s.handleCreateMap))
	r.HandleFunc("DELETE", "/api/admin/scim/group-maps/{id}", contract.PermSCIMManage, rbac.SessionOnly(s.audit, s.txs, s.handleDeleteMap))
}

// MapView 는 매핑 한 줄이다.
type MapView struct {
	ID              int64  `json:"id"`
	Source          string `json:"source"`
	GroupExternalID string `json:"groupExternalId"`
	DisplayName     string `json:"displayName"`
	Kind            string `json:"kind"`
	UnitID          int64  `json:"unitId,omitempty"`
	UnitCode        string `json:"unitCode,omitempty"`
	UnitName        string `json:"unitName,omitempty"`
	RoleID          int64  `json:"roleId,omitempty"`
	RoleName        string `json:"roleName,omitempty"`
	Members         int    `json:"members"`
	CreatedAt       string `json:"createdAt"`
}

// UnmappedGroup 은 IdP 가 보냈지만 매핑이 없어 거부된 그룹이다(동기화 기록에서 읽는다).
type UnmappedGroup struct {
	Source          string `json:"source"`
	GroupExternalID string `json:"groupExternalId"`
	DisplayName     string `json:"displayName"`
	At              string `json:"at"`
}

// TargetUnit·TargetRole 은 매핑 대상 선택지다. 위험 권한이 든 역할은 선택할 수 없다(Risky).
type TargetUnit struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}
type TargetRole struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Risky bool   `json:"risky"`
}

// MapList 는 매핑 화면의 재료다.
type MapList struct {
	Mode     string          `json:"mode"`
	Maps     []MapView       `json:"maps"`
	Unmapped []UnmappedGroup `json:"unmapped"`
	Units    []TargetUnit    `json:"units"`
	Roles    []TargetRole    `json:"roles"`
}

func (s *Service) ListMaps(ctx context.Context, p contract.Principal) (MapList, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermSCIMRead); err != nil {
		return MapList{}, err
	}
	st, err := s.loadSettings(ctx, s.db)
	if err != nil {
		return MapList{}, err
	}
	out := MapList{Mode: st.GroupMode, Maps: []MapView{}, Unmapped: []UnmappedGroup{}, Units: []TargetUnit{}, Roles: []TargetRole{}}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.source, m.group_external_id, m.display_name, m.kind, m.unit_id, m.role_id, m.created_at,
		(SELECT COUNT(*) FROM scim_group_members gm WHERE gm.map_id = m.id), COALESCE(r.name, '') FROM scim_group_maps m LEFT JOIN roles r ON r.id = m.role_id ORDER BY m.id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v MapView
		var u, ro sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Source, &v.GroupExternalID, &v.DisplayName, &v.Kind, &u, &ro, &v.CreatedAt, &v.Members, &v.RoleName); err != nil {
			rows.Close()
			return out, err
		}
		v.UnitID, v.RoleID = u.Int64, ro.Int64
		out.Maps = append(out.Maps, v)
	}
	rows.Close()
	day := string(s.org.Today())
	urows, err := s.db.QueryContext(ctx, `SELECT unit_id, code, name FROM org_unit_spans WHERE closed = 0 AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to) ORDER BY code`, day, day)
	if err != nil {
		return out, err
	}
	for urows.Next() {
		var u TargetUnit
		if err := urows.Scan(&u.ID, &u.Code, &u.Name); err != nil {
			urows.Close()
			return out, err
		}
		out.Units = append(out.Units, u)
	}
	urows.Close()
	for i := range out.Maps {
		for _, u := range out.Units {
			if u.ID == out.Maps[i].UnitID {
				out.Maps[i].UnitCode, out.Maps[i].UnitName = u.Code, u.Name
			}
		}
	}
	rrows, err := s.db.QueryContext(ctx, `SELECT id, key, name FROM roles WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return out, err
	}
	var roles []TargetRole
	for rrows.Next() {
		var t TargetRole
		if err := rrows.Scan(&t.ID, &t.Key, &t.Name); err != nil {
			rrows.Close()
			return out, err
		}
		roles = append(roles, t)
	}
	rrows.Close()
	for _, t := range roles {
		risky, err := access.RoleRisk(ctx, s.db, t.ID)
		if err != nil {
			return out, err
		}
		t.Risky = len(risky) > 0
		out.Roles = append(out.Roles, t)
	}
	// 매핑이 없어 거부된 그룹(최근 것부터, 이미 매핑된 것은 뺀다).
	lrows, err := s.db.QueryContext(ctx, `SELECT source, external_id, summary, at FROM scim_sync_log WHERE resource = 'Group' AND op = 'rejected' AND external_id <> '' ORDER BY id DESC LIMIT 300`)
	if err != nil {
		return out, err
	}
	defer lrows.Close()
	seen := map[string]bool{}
	for _, m := range out.Maps {
		seen[m.Source+"|"+m.GroupExternalID] = true
	}
	for lrows.Next() && len(out.Unmapped) < 20 {
		var g UnmappedGroup
		var sum string
		if err := lrows.Scan(&g.Source, &g.GroupExternalID, &sum, &g.At); err != nil {
			return out, err
		}
		k := g.Source + "|" + g.GroupExternalID
		if seen[k] {
			continue
		}
		seen[k] = true
		g.DisplayName = g.GroupExternalID
		out.Unmapped = append(out.Unmapped, g)
	}
	return out, lrows.Err()
}

// MapInput 은 매핑 생성 입력이다. 대상은 연동 방식에 따라 UnitID(조직 모드) 또는 RoleID(역할 모드) 하나다.
type MapInput struct {
	Source          string `json:"source"`
	GroupExternalID string `json:"groupExternalId"`
	DisplayName     string `json:"displayName"`
	UnitID          int64  `json:"unitId"`
	RoleID          int64  `json:"roleId"`
	// Scope 는 역할 모드 부여의 범위다: own(소속 법인 전체, 비우면 이것) | entity(ScopeEntityID 의 법인 하나). group 은 2인 확인이 필요해 SCIM 으로 줄 수 없다.
	Scope         string `json:"scope,omitempty"`
	ScopeEntityID int64  `json:"scopeEntityId,omitempty"`
}

func (s *Service) CreateMap(ctx context.Context, p contract.Principal, in MapInput) (MapView, error) {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return MapView{}, err
	}
	in.Source, in.GroupExternalID, in.DisplayName = strings.TrimSpace(in.Source), strings.TrimSpace(in.GroupExternalID), strings.TrimSpace(in.DisplayName)
	if !inList(Sources, in.Source) {
		return MapView{}, verr("출처(IdP)를 고르세요.")
	}
	if in.GroupExternalID == "" || utf8.RuneCountInString(in.GroupExternalID) > 255 {
		return MapView{}, verr("그룹 식별자는 1~255자여야 합니다.")
	}
	if utf8.RuneCountInString(in.DisplayName) > 200 {
		return MapView{}, verr("표시 이름은 200자 이내여야 합니다.")
	}
	if in.DisplayName == "" {
		in.DisplayName = in.GroupExternalID
	}
	actor := s.webActor(p)
	var id int64
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		st, err := s.loadSettings(ctx, tx)
		if err != nil {
			return err
		}
		var unit, role any
		target := ""
		switch st.GroupMode {
		case "unit":
			var name, code string
			day := string(s.org.Today())
			if err := tx.QueryRowContext(ctx, `SELECT name, code FROM org_unit_spans WHERE unit_id = ? AND closed = 0 AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)`, in.UnitID, day, day).Scan(&name, &code); err != nil {
				return verr("조직을 고르세요(폐지된 조직은 연결할 수 없습니다).")
			}
			unit, target = in.UnitID, name
			in.Scope, in.ScopeEntityID = contract.ScopeOwn, 0 // 조직 모드에는 부여 범위가 없다
		case "role":
			risky, err := access.RoleRisk(ctx, tx, in.RoleID)
			if errors.Is(err, contract.ErrNotFound) {
				return verr("역할을 고르세요.")
			}
			if err != nil {
				return err
			}
			if len(risky) > 0 {
				return &access.RiskyRoleError{Perms: risky} // 위험 권한이 든 역할은 SCIM 으로 줄 수 없다(B33)
			}
			var rn string
			_ = tx.QueryRowContext(ctx, `SELECT name FROM roles WHERE id = ?`, in.RoleID).Scan(&rn)
			role, target = in.RoleID, rn
			switch in.Scope {
			case "", contract.ScopeOwn:
				in.Scope, in.ScopeEntityID = contract.ScopeOwn, 0
			case contract.ScopeEntity:
				var active int
				if err := tx.QueryRowContext(ctx, `SELECT active FROM entities WHERE id = ?`, in.ScopeEntityID).Scan(&active); err != nil || active != 1 {
					return verr("범위로 쓸 활성 법인을 고르세요.")
				}
			default:
				return verr("SCIM 그룹 매핑의 범위는 소속 법인 전체 또는 법인 하나만 쓸 수 있습니다(그룹 전체는 사람이 2인 확인으로 부여합니다).")
			}
		default:
			return verr("먼저 그룹 연동 방식(조직 또는 역할)을 정하세요.")
		}
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO scim_group_maps (source, group_external_id, display_name, kind, unit_id, role_id, created_by, created_at, scope, scope_entity_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			in.Source, in.GroupExternalID, in.DisplayName, st.GroupMode, unit, role, p.UserID, s.stamp(), mapScope(in), in.ScopeEntityID)
		if err != nil {
			if dbutil.IsUnique(err) {
				return stateError("이미 매핑된 그룹입니다")
			}
			return err
		}
		return s.record(ctx, tx, actor, "scim.group_map_set", audit.TargetSetting, strconv.FormatInt(id, 10), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "source", New: in.Source}, {Key: "scim_group", New: in.DisplayName}, {Key: "map_kind", New: st.GroupMode}, {Key: "map_target", New: target}}, "")
	})
	if err != nil {
		return MapView{}, err
	}
	return MapView{ID: id, Source: in.Source, GroupExternalID: in.GroupExternalID, DisplayName: in.DisplayName}, nil
}

// DeleteMap 은 매핑을 지운다. 역할 매핑이면 SCIM 이 만든 부여를 모두 회수한다(사람이 따로 준 부여는 건드리지 않는다).
func (s *Service) DeleteMap(ctx context.Context, p contract.Principal, id int64) error {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return err
	}
	actor := s.webActor(p)
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		m, err := s.scanMap(tx.QueryRowContext(ctx, `SELECT `+mapCols+` FROM scim_group_maps m WHERE m.id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		mem, err := s.members(ctx, tx, id)
		if err != nil {
			return err
		}
		revoked := 0
		for _, c := range mem {
			if m.Kind == "role" && c.Grant.Valid {
				var others int // 다른 그룹 매핑도 같은 부여를 가리키면 그쪽이 지워질 때 회수한다
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_group_members WHERE grant_id = ? AND map_id <> ?`, c.Grant.Int64, id).Scan(&others); err != nil {
					return err
				}
				if others > 0 {
					continue
				}
				if err := s.access.SCIMRevoke(ctx, tx, actor, c.Grant.Int64); err != nil {
					return err
				}
				revoked++
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_members WHERE map_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_maps WHERE id = ?`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, "scim.group_map_delete", audit.TargetSetting, strconv.FormatInt(id, 10), audit.ResultSuccess,
			[]audit.DiffItem{{Key: "source", Old: m.Source}, {Key: "scim_group", Old: m.Display}, {Key: "map_kind", Old: m.Kind}, {Key: "revoked_grants", New: strconv.Itoa(revoked)}}, "")
	})
}

func (s *Service) handleListMaps(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	out, err := s.ListMaps(r.Context(), p)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleCreateMap(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in MapInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "요청 형식이 올바르지 않습니다"})
		return
	}
	v, err := s.CreateMap(r.Context(), p, in)
	if err != nil {
		writeAdminErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Service) handleDeleteMap(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAdminErr(w, contract.ErrNotFound)
		return
	}
	if err := s.DeleteMap(r.Context(), p, id); err != nil {
		writeAdminErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mapScope 는 조직 모드(범위 없음)면 기본값 own 을 준다.
func mapScope(in MapInput) string {
	if in.Scope == "" {
		return contract.ScopeOwn
	}
	return in.Scope
}
