package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/choibongseok/procure-ledger/internal/sod"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

const requestTTL = 24 * time.Hour

// Service 는 역할·부여 관리(B33 3단계)다. 모든 쓰기는 같은 트랜잭션의 감사 기록과 함께이고, 기록이 실패하면 요청도 실패한다.
// 권한은 role:manage(세션 전용 라우트)다. 핸들러 안에서 역할 이름을 비교하지 않는다.
type Service struct {
	db    *sql.DB
	txs   contract.TxManager
	audit audit.Recorder
	now   func() time.Time
	sod   sod.Guard // 권한 분리 판정(B41). nil 이면 판정하지 않는다(시험·부분 조립)
}

// SetSoD 는 권한 분리 판정기를 꽂는다. 판정기는 access 보다 늦게 만들어져 조립 때 나중에 붙인다.
func (s *Service) SetSoD(g sod.Guard) { s.sod = g }

// holdBefore 는 변경 전 hold 규칙 보유 상태다(판정기가 없으면 nil).
func (s *Service) holdBefore(ctx context.Context, tx contract.Tx) (sod.HoldSet, error) {
	if s.sod == nil {
		return nil, nil
	}
	return s.sod.SnapshotHolds(ctx, tx)
}

// holdCheck 는 실효 권한을 다시 계산한 직후 부른다: 변경으로 새로 생긴 쌍을 규칙 모드대로 판정한다.
func (s *Service) holdCheck(ctx context.Context, tx contract.Tx, actor contract.Principal, before sod.HoldSet) error {
	if s.sod == nil {
		return nil
	}
	return s.sod.CheckNewHolds(ctx, tx, actor, before)
}

// noteBlocked 는 WithTx 가 끝난 뒤 SoD 차단이면 롤백 밖에 기록한다.
func (s *Service) noteBlocked(ctx context.Context, err error) error {
	if s.sod != nil {
		sod.NoteBlocked(ctx, s.sod, err)
	}
	return err
}

// New 는 Service 를 만든다.
func New(db *sql.DB, txs contract.TxManager, rec audit.Recorder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, txs: txs, audit: rec, now: now}
}

func (s *Service) today() contract.Day { return contract.DayOf(s.now()) }
func (s *Service) stamp() string       { return s.now().UTC().Format(time.RFC3339Nano) }

var systemActor = contract.Principal{Role: "system", Channel: contract.ChannelWeb}

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}
func serr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrInvalidState, fmt.Sprintf(format, a...))
}

func (s *Service) authorize(ctx context.Context, p contract.Principal) error {
	_, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermRoleManage)
	return err
}

func (s *Service) record(ctx context.Context, tx contract.Tx, actor contract.Principal, action string, tt audit.TargetType, id string, diff []audit.DiffItem, detail string) error {
	if actor.Channel == "" {
		actor.Channel = contract.ChannelWeb
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: action, TargetType: tt, TargetID: id, Result: audit.ResultSuccess, Diff: diff, Detail: detail})
}

// ---- 권한 메타 ----

// PermInfo 는 화면의 권한 선택에 쓰는 메타다(한국어 라벨은 화면이 권한 코드로 가진다).
type PermInfo struct {
	Code  string `json:"code"`
	Group string `json:"group"`
	Risk  bool   `json:"risk"`
}

// Permissions 는 선택 가능한 권한 전체(public·authenticated·setup 은 제외)와 위험 표시다.
func Permissions() []PermInfo {
	risk := map[contract.Permission]bool{}
	for _, p := range contract.HighRiskPermissions {
		risk[p] = true
	}
	out := make([]PermInfo, 0, len(contract.AllPermissions))
	for _, p := range contract.AllPermissions {
		g, _, _ := strings.Cut(string(p), ":")
		out = append(out, PermInfo{Code: string(p), Group: g, Risk: risk[p]})
	}
	return out
}

func knownPerm(p string) bool {
	for _, k := range contract.AllPermissions {
		if string(k) == p {
			return true
		}
	}
	return false
}

func isRisk(p string) bool {
	for _, k := range contract.HighRiskPermissions {
		if string(k) == p {
			return true
		}
	}
	return false
}

func riskOf(perms []string) []string {
	var out []string
	for _, p := range perms {
		if isRisk(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ---- 역할 ----

// RoleView 는 역할 한 건이다.
type RoleView struct {
	ID          int64    `json:"id"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"`
	Version     int64    `json:"version"`
	Permissions []string `json:"permissions"`
	GrantCount  int      `json:"grantCount"`
	UserCount   int      `json:"userCount"`
}

func loadRole(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, id int64) (RoleView, error) {
	var r RoleView
	err := q.QueryRowContext(ctx, `SELECT id, key, name, description, kind, version,
		(SELECT COUNT(*) FROM role_grants g WHERE g.role_id = roles.id),
		(SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users uu ON uu.id = e.user_id AND uu.active = 1 WHERE e.role_id = roles.id)
		FROM roles WHERE id = ? AND deleted_at IS NULL`, id).Scan(&r.ID, &r.Key, &r.Name, &r.Description, &r.Kind, &r.Version, &r.GrantCount, &r.UserCount)
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	rows, err := q.QueryContext(ctx, `SELECT permission FROM role_permissions WHERE role_id = ? ORDER BY permission`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Permissions = []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return r, err
		}
		r.Permissions = append(r.Permissions, p)
	}
	return r, rows.Err()
}

// ListRoles 는 삭제되지 않은 역할 전부(템플릿 먼저)다.
func (s *Service) ListRoles(ctx context.Context, p contract.Principal) ([]RoleView, error) {
	if err := s.authorize(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM roles WHERE deleted_at IS NULL ORDER BY CASE kind WHEN 'template' THEN 0 ELSE 1 END, id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]RoleView, 0, len(ids))
	for _, id := range ids {
		r, err := loadRole(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func cleanName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", verr("역할 이름을 입력하세요")
	}
	if len([]rune(n)) > 100 {
		return "", verr("역할 이름은 100자 이하여야 합니다")
	}
	if strings.ContainsAny(n, "\r\n<>") {
		return "", verr("역할 이름에 줄바꿈이나 <, > 를 쓸 수 없습니다")
	}
	return n, nil
}

func cleanPerms(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		if !knownPerm(p) {
			return nil, verr("알 수 없는 권한입니다: %q", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func setPerms(ctx context.Context, tx contract.Tx, roleID int64, perms []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	for _, p := range perms {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_permissions (role_id, permission) VALUES (?,?)`, roleID, p); err != nil {
			return err
		}
	}
	return nil
}

func diffSets(old, nw []string) (added, removed []string) {
	o, n := map[string]bool{}, map[string]bool{}
	for _, p := range old {
		o[p] = true
	}
	for _, p := range nw {
		n[p] = true
		if !o[p] {
			added = append(added, p)
		}
	}
	for _, p := range old {
		if !n[p] {
			removed = append(removed, p)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

func nameTaken(ctx context.Context, tx contract.Tx, name string, exceptID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE name = ? AND id <> ?`, name, exceptID).Scan(&n)
	return n > 0, err
}

// CreateRole 은 사용자 정의 역할을 만든다. 권한이 비어 있어도 된다(부여해도 아무 권한이 없다).
func (s *Service) CreateRole(ctx context.Context, p contract.Principal, name, desc string, perms []string) (RoleView, error) {
	if err := s.authorize(ctx, p); err != nil {
		return RoleView{}, err
	}
	n, err := cleanName(name)
	if err != nil {
		return RoleView{}, err
	}
	ps, err := cleanPerms(perms)
	if err != nil {
		return RoleView{}, err
	}
	if len([]rune(desc)) > 500 {
		return RoleView{}, verr("설명은 500자 이하여야 합니다")
	}
	var out RoleView
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if taken, err := nameTaken(ctx, tx, n, 0); err != nil {
			return err
		} else if taken {
			return verr("같은 이름의 역할이 이미 있습니다")
		}
		now := s.stamp()
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO roles (key, name, description, kind, created_by, created_at, updated_by, updated_at) VALUES (?,?,?,'custom',?,?,?,?)`,
			fmt.Sprintf("tmp_%d", s.now().UnixNano()), n, strings.TrimSpace(desc), p.UserID, now, p.UserID, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roles SET key = ? WHERE id = ?`, fmt.Sprintf("r_%d", id), id); err != nil {
			return err
		}
		if err := setPerms(ctx, tx, id, ps); err != nil {
			return err
		}
		out, err = loadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "role.create", audit.TargetRole, fmt.Sprint(id),
			[]audit.DiffItem{{Key: "role_name", New: n}, {Key: "description", New: strings.TrimSpace(desc)}, {Key: "permissions_added", New: strings.Join(ps, ",")}}, "")
	})
	return out, err
}

// CloneRole 은 역할(템플릿 포함)을 복제해 사용자 정의 역할을 만든다.
func (s *Service) CloneRole(ctx context.Context, p contract.Principal, id int64, name string) (RoleView, error) {
	src, err := loadRole(ctx, s.db, id)
	if err != nil {
		return RoleView{}, err
	}
	return s.CreateRole(ctx, p, name, src.Description, src.Permissions)
}

// RoleChange 는 UpdateRole 의 결과다. Request 가 있으면 즉시 적용되지 않고 2인 확인을 기다린다.
type RoleChange struct {
	Role    *RoleView `json:"role,omitempty"`
	Request *Request  `json:"request,omitempty"`
}

// UpdateRole 은 사용자 정의 역할의 이름·설명·권한을 바꾼다(낙관적 잠금 version). 기본 템플릿은 고칠 수 없다.
// 이미 부여된 역할에 위험 권한을 더하는 변경은 다른 role:manage 보유자의 승인(24시간)이 있어야 적용된다(보유자가 한 명뿐이면 경고로 대체).
func (s *Service) UpdateRole(ctx context.Context, p contract.Principal, id, version int64, name, desc string, perms []string, reason string) (RoleChange, error) {
	if err := s.authorize(ctx, p); err != nil {
		return RoleChange{}, err
	}
	n, err := cleanName(name)
	if err != nil {
		return RoleChange{}, err
	}
	ps, err := cleanPerms(perms)
	if err != nil {
		return RoleChange{}, err
	}
	var out RoleChange
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := loadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Kind == "template" {
			return serr("기본 템플릿은 고칠 수 없습니다. 복제해서 사용자 정의 역할로 쓰세요")
		}
		if cur.Version != version {
			return serr("다른 관리자가 먼저 이 역할을 고쳤습니다. 다시 불러온 뒤 고치세요")
		}
		if taken, err := nameTaken(ctx, tx, n, id); err != nil {
			return err
		} else if taken {
			return verr("같은 이름의 역할이 이미 있습니다")
		}
		added, removed := diffSets(cur.Permissions, ps)
		if risky := riskOf(added); len(risky) > 0 && cur.GrantCount > 0 {
			solo, err := s.soloHolder(ctx, tx, p)
			if err != nil {
				return err
			}
			if !solo {
				if strings.TrimSpace(reason) == "" {
					return verr("위험 권한(%s)을 더하려면 사유를 입력해야 합니다", strings.Join(risky, ", "))
				}
				req, err := s.createRequest(ctx, tx, p, "role_update", reqPayload{RoleID: id, Version: version, Name: n, Description: strings.TrimSpace(desc), Permissions: ps}, reason, risky)
				if err != nil {
					return err
				}
				out.Request = &req
				return nil
			}
		}
		return s.guardHolders(ctx, tx, func() error {
			solo := false
			if risky := riskOf(added); len(risky) > 0 && cur.GrantCount > 0 {
				solo = true
			}
			if err := s.applyRoleUpdate(ctx, tx, p, cur, n, strings.TrimSpace(desc), ps, added, removed, solo, 0); err != nil {
				return err
			}
			r, err := loadRole(ctx, tx, id)
			out.Role = &r
			return err
		})
	})
	return out, s.noteBlocked(ctx, err)
}

func (s *Service) applyRoleUpdate(ctx context.Context, tx contract.Tx, actor contract.Principal, cur RoleView, name, desc string, ps, added, removed []string, solo bool, approvedBy int64) error {
	holds, err := s.holdBefore(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roles SET name = ?, description = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
		name, desc, actor.UserID, s.stamp(), cur.ID); err != nil {
		return err
	}
	if err := setPerms(ctx, tx, cur.ID, ps); err != nil {
		return err
	}
	if err := Recompute(ctx, tx, s.today()); err != nil {
		return err
	}
	if err := s.holdCheck(ctx, tx, actor, holds); err != nil {
		return err
	}
	diff := []audit.DiffItem{}
	if name != cur.Name {
		diff = append(diff, audit.DiffItem{Key: "role_name", Old: cur.Name, New: name})
	} else {
		diff = append(diff, audit.DiffItem{Key: "role_name", New: name}) // 이름이 그대로여도 어느 역할의 변경인지 남긴다
	}
	if desc != cur.Description {
		diff = append(diff, audit.DiffItem{Key: "description", Old: cur.Description, New: desc})
	}
	if len(added) > 0 {
		diff = append(diff, audit.DiffItem{Key: "permissions_added", New: strings.Join(added, ",")})
	}
	if len(removed) > 0 {
		diff = append(diff, audit.DiffItem{Key: "permissions_removed", New: strings.Join(removed, ",")})
	}
	if cur.GrantCount > 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users uu ON uu.id = e.user_id AND uu.active = 1 WHERE e.role_id = ?`, cur.ID).Scan(&n); err != nil {
			return err
		}
		diff = append(diff, audit.DiffItem{Key: "affected_users", New: fmt.Sprint(n)})
	}
	if solo {
		diff = append(diff, audit.DiffItem{Key: "solo_admin", New: "true"})
	}
	if approvedBy != 0 {
		diff = append(diff, audit.DiffItem{Key: "approved_by", New: fmt.Sprint(approvedBy)})
	}
	if len(diff) == 0 {
		return nil
	}
	return s.record(ctx, tx, actor, "role.update", audit.TargetRole, fmt.Sprint(cur.ID), diff, "")
}

// DeleteRole 은 부여가 없는 사용자 정의 역할을 지운다(이름은 재사용할 수 있게 바꿔 둔다).
func (s *Service) DeleteRole(ctx context.Context, p contract.Principal, id int64) error {
	if err := s.authorize(ctx, p); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := loadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Kind == "template" {
			return serr("기본 템플릿은 지울 수 없습니다")
		}
		if cur.GrantCount > 0 {
			return serr("이 역할이 부여되어 있습니다(%d건). 부여를 먼저 회수하세요", cur.GrantCount)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roles SET deleted_at = ?, name = name || ' (삭제 #' || id || ')' WHERE id = ?`, s.stamp(), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "role.delete", audit.TargetRole, fmt.Sprint(id), []audit.DiffItem{{Key: "role_name", Old: cur.Name}}, "")
	})
}

// ---- 부여 ----

// GrantInput 은 부여 대상이다.
type GrantInput struct {
	TargetType         string `json:"targetType"` // user | unit | rank | unit_head
	TargetID           int64  `json:"targetId"`
	UnitKind           string `json:"unitKind"` // unit_head 일 때 조직 종류
	IncludeDescendants bool   `json:"includeDescendants"`
	Reason             string `json:"reason"`
	// Scope 는 부여가 효력을 갖는 법인이다(B44): own(소속·겸직 법인 전체, 비우면 이것) | entity(ScopeEntityID 의 법인 하나) | group(모든 법인).
	Scope         string `json:"scope,omitempty"`
	ScopeEntityID int64  `json:"scopeEntityId,omitempty"`
}

// scope 는 범위 값을 정규화한다(비면 own).
func (g GrantInput) scope() (string, int64) {
	switch g.Scope {
	case contract.ScopeEntity:
		return contract.ScopeEntity, g.ScopeEntityID
	case contract.ScopeGroup:
		return contract.ScopeGroup, 0
	}
	return contract.ScopeOwn, 0
}

func (g GrantInput) validate(ctx context.Context, tx contract.Tx) error {
	exists := func(q string, id int64) (bool, error) {
		var n int
		err := tx.QueryRowContext(ctx, q, id).Scan(&n)
		return n > 0, err
	}
	switch g.TargetType {
	case "user":
		if ok, err := exists(`SELECT COUNT(*) FROM users WHERE id = ?`, g.TargetID); err != nil || !ok {
			if err != nil {
				return err
			}
			return verr("대상 사용자를 찾을 수 없습니다")
		}
	case "unit":
		if ok, err := exists(`SELECT COUNT(*) FROM org_units WHERE id = ?`, g.TargetID); err != nil || !ok {
			if err != nil {
				return err
			}
			return verr("대상 조직을 찾을 수 없습니다")
		}
	case "rank":
		if ok, err := exists(`SELECT COUNT(*) FROM org_ranks WHERE id = ?`, g.TargetID); err != nil || !ok {
			if err != nil {
				return err
			}
			return verr("대상 직위를 찾을 수 없습니다")
		}
	case "unit_head":
		switch g.UnitKind {
		case "company", "division", "department", "team":
		default:
			return verr("조직 종류(회사·본부·부서·팀)를 고르세요")
		}
	default:
		return verr("부여 대상 유형이 올바르지 않습니다")
	}
	if g.TargetType != "unit" && g.IncludeDescendants {
		return verr("하위 조직 포함은 조직 부여에만 있습니다")
	}
	switch g.Scope {
	case "", contract.ScopeOwn, contract.ScopeGroup:
		if g.ScopeEntityID != 0 {
			return verr("법인은 적용 범위가 '특정 법인'일 때만 고를 수 있습니다")
		}
	case contract.ScopeEntity:
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(active), -1) FROM entities WHERE id = ?`, g.ScopeEntityID).Scan(&active); err != nil {
			return err
		}
		if active < 0 {
			return verr("적용할 법인을 찾을 수 없습니다")
		}
		if active == 0 {
			return verr("비활성 법인에는 부여할 수 없습니다")
		}
	default:
		return verr("적용 범위가 올바르지 않습니다")
	}
	if len([]rune(g.Reason)) > 300 {
		return verr("사유는 300자 이하여야 합니다")
	}
	return nil
}

func (g GrantInput) key() (targetID int64, kind string, incl int) {
	targetID, kind = g.TargetID, ""
	if g.TargetType == "unit_head" {
		targetID, kind = 0, g.UnitKind
	}
	if g.IncludeDescendants {
		incl = 1
	}
	return
}

// GrantResult 는 부여 결과다. Request 가 있으면 2인 확인을 기다린다.
type GrantResult struct {
	GrantID       int64    `json:"grantId,omitempty"`
	AffectedUsers int      `json:"affectedUsers"`
	Request       *Request `json:"request,omitempty"`
	Solo          bool     `json:"solo,omitempty"`
}

func (s *Service) roleHasRisk(ctx context.Context, tx contract.Tx, roleID int64) ([]string, error) {
	r, err := loadRole(ctx, tx, roleID)
	if err != nil {
		return nil, err
	}
	return riskOf(r.Permissions), nil
}

// AddGrant 는 역할을 대상에게 준다. 위험 권한이 든 역할의 새 부여는 다른 role:manage 보유자의 승인이 있어야 적용된다(보유자가 한 명뿐이면 경고·사유로 대체).
func (s *Service) AddGrant(ctx context.Context, p contract.Principal, roleID int64, in GrantInput) (GrantResult, error) {
	if err := s.authorize(ctx, p); err != nil {
		return GrantResult{}, err
	}
	var out GrantResult
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := in.validate(ctx, tx); err != nil {
			return err
		}
		risky, err := s.roleHasRisk(ctx, tx, roleID)
		if err != nil {
			return err
		}
		if sc, _ := in.scope(); sc == contract.ScopeGroup { // 그룹 전체 범위 부여는 모든 법인에서 효력이 있어 위험 부여와 같이 2인 확인(B44 D7)
			risky = append(risky, "그룹 전체 범위")
		}
		solo := false
		if len(risky) > 0 {
			if solo, err = s.soloHolder(ctx, tx, p); err != nil {
				return err
			}
			if !solo {
				if strings.TrimSpace(in.Reason) == "" {
					return verr("위험 권한(%s)이 든 역할의 부여는 사유를 입력해야 합니다", strings.Join(risky, ", "))
				}
				cur, err := loadRole(ctx, tx, roleID)
				if err != nil {
					return err
				}
				// 요청 시점의 역할 버전을 저장한다: 승인 전에 역할 권한이 바뀌었으면 승인할 수 없다(role_update 와 같은 방식).
				req, err := s.createRequest(ctx, tx, p, "grant", reqPayload{RoleID: roleID, Grant: &in, Version: cur.Version}, in.Reason, risky)
				if err != nil {
					return err
				}
				out.Request = &req
				return nil
			}
			if strings.TrimSpace(in.Reason) == "" {
				return verr("관리자가 한 명뿐이라 2인 확인 없이 적용됩니다. 위험 권한(%s) 부여의 사유를 입력하세요", strings.Join(risky, ", "))
			}
		}
		id, n, err := s.applyGrant(ctx, tx, p, roleID, in, solo, 0)
		out.GrantID, out.AffectedUsers, out.Solo = id, n, solo
		return err
	})
	return out, s.noteBlocked(ctx, err)
}

func (s *Service) applyGrant(ctx context.Context, tx contract.Tx, actor contract.Principal, roleID int64, in GrantInput, solo bool, approvedBy int64) (int64, int, error) {
	role, err := loadRole(ctx, tx, roleID)
	if err != nil {
		return 0, 0, err
	}
	tid, kind, incl := in.key()
	holds, err := s.holdBefore(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	scope, scopeEntity := in.scope()
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, reason, created_by, created_at, scope, scope_entity_id) VALUES (?,?,?,?,?,'manual',?,?,?,?,?)`,
		roleID, in.TargetType, tid, kind, incl, strings.TrimSpace(in.Reason), actor.UserID, s.stamp(), scope, scopeEntity)
	if err != nil {
		if dbutil.IsUnique(err) {
			return 0, 0, serr("이미 같은 대상에게 이 역할이 부여되어 있습니다")
		}
		return 0, 0, err
	}
	if err := Recompute(ctx, tx, s.today()); err != nil {
		return 0, 0, err
	}
	if s.sod != nil { // 자기 자신에게 역할 부여(role_self_grant): 직접 대상뿐 아니라 내가 속한 조직·직위·조직장 부여로 내게 권한이 들어오는 경우도 같다
		self := in.TargetType == "user" && tid == actor.UserID
		if !self {
			var c int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_effective_permissions WHERE grant_id = ? AND user_id = ?`, id, actor.UserID).Scan(&c); err != nil {
				return 0, 0, err
			}
			self = c > 0
		}
		if self {
			if _, err := s.sod.Evaluate(ctx, tx, sod.Check{Rule: sod.RuleRoleSelfGrant, Actor: actor, Subject: sod.Ref{Type: "role_grant", ID: roleID}, Other: actor.UserID}); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := s.holdCheck(ctx, tx, actor, holds); err != nil {
		return 0, 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users uu ON uu.id = e.user_id AND uu.active = 1 WHERE e.grant_id = ?`, id).Scan(&n); err != nil {
		return 0, 0, err
	}
	diff := []audit.DiffItem{{Key: "role_name", New: role.Name}, {Key: "grant_target_type", New: in.TargetType}, {Key: "grant_target", New: s.targetLabel(ctx, tx, in.TargetType, tid, kind)},
		{Key: "affected_users", New: fmt.Sprint(n)}}
	if incl == 1 {
		diff = append(diff, audit.DiffItem{Key: "include_descendants", New: "true"})
	}
	if scope != contract.ScopeOwn {
		diff = append(diff, audit.DiffItem{Key: "grant_scope", New: s.scopeLabel(ctx, tx, scope, scopeEntity)})
	}
	if r := strings.TrimSpace(in.Reason); r != "" {
		diff = append(diff, audit.DiffItem{Key: "grant_reason", New: r})
	}
	if solo {
		diff = append(diff, audit.DiffItem{Key: "solo_admin", New: "true"})
	}
	if approvedBy != 0 {
		diff = append(diff, audit.DiffItem{Key: "approved_by", New: fmt.Sprint(approvedBy)})
	}
	return id, n, s.record(ctx, tx, actor, "role.grant", audit.TargetRoleGrant, fmt.Sprint(id), diff, "")
}

// RevokeGrant 는 부여를 회수한다(즉시 적용). 마지막 role:manage·auth:manage 보유자가 없어지는 회수는 거부한다.
func (s *Service) RevokeGrant(ctx context.Context, p contract.Principal, grantID int64) error {
	if err := s.authorize(ctx, p); err != nil {
		return err
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var roleID, target, incl int64
		var ttype, kind, src string
		err := tx.QueryRowContext(ctx, `SELECT role_id, target_type, target_id, unit_kind, include_descendants, source FROM role_grants WHERE id = ?`, grantID).
			Scan(&roleID, &ttype, &target, &kind, &incl, &src)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrNotFound
		}
		if err != nil {
			return err
		}
		role, err := loadRole(ctx, tx, roleID)
		if err != nil {
			return err
		}
		return s.guardHolders(ctx, tx, func() error {
			var before int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users uu ON uu.id = e.user_id AND uu.active = 1 WHERE e.grant_id = ?`, grantID).Scan(&before); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM role_grants WHERE id = ?`, grantID); err != nil {
				return err
			}
			if err := Recompute(ctx, tx, s.today()); err != nil {
				return err
			}
			diff := []audit.DiffItem{{Key: "role_name", Old: role.Name}, {Key: "grant_target_type", Old: ttype}, {Key: "grant_target", Old: s.targetLabel(ctx, tx, ttype, target, kind)}, {Key: "affected_users", New: fmt.Sprint(before)}}
			return s.record(ctx, tx, p, "role.revoke", audit.TargetRoleGrant, fmt.Sprint(grantID), diff, "")
		})
	})
}

// GrantView 는 부여 목록의 한 줄이다.
type GrantView struct {
	ID                 int64  `json:"id"`
	RoleID             int64  `json:"roleId"`
	RoleName           string `json:"roleName"`
	TargetType         string `json:"targetType"`
	TargetID           int64  `json:"targetId"`
	UnitKind           string `json:"unitKind,omitempty"`
	IncludeDescendants bool   `json:"includeDescendants"`
	Label              string `json:"label"`
	Source             string `json:"source"`
	Reason             string `json:"reason"`
	Users              int    `json:"users"`
	CreatedAt          string `json:"createdAt"`
	Scope              string `json:"scope"`
	ScopeEntityID      int64  `json:"scopeEntityId,omitempty"`
	ScopeLabel         string `json:"scopeLabel"` // 적용 범위의 한국어 이름(소속 법인 전체·○○ 법인·그룹 전체)
}

// ListGrants 는 역할의 부여 목록이다(roleID 0 이면 전부).
func (s *Service) ListGrants(ctx context.Context, p contract.Principal, roleID int64) ([]GrantView, error) {
	if err := s.authorize(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.role_id, r.name, g.target_type, g.target_id, g.unit_kind, g.include_descendants, g.source, g.reason, g.created_at, g.scope, g.scope_entity_id,
		(SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users uu ON uu.id = e.user_id AND uu.active = 1 WHERE e.grant_id = g.id)
		FROM role_grants g JOIN roles r ON r.id = g.role_id WHERE r.deleted_at IS NULL AND (? = 0 OR g.role_id = ?) ORDER BY g.id`, roleID, roleID)
	if err != nil {
		return nil, err
	}
	var out []GrantView
	for rows.Next() {
		var g GrantView
		var incl int
		if err := rows.Scan(&g.ID, &g.RoleID, &g.RoleName, &g.TargetType, &g.TargetID, &g.UnitKind, &incl, &g.Source, &g.Reason, &g.CreatedAt, &g.Scope, &g.ScopeEntityID, &g.Users); err != nil {
			rows.Close()
			return nil, err
		}
		g.IncludeDescendants = incl == 1
		out = append(out, g)
	}
	rows.Close()
	for i := range out {
		out[i].Label = s.targetLabel(ctx, s.db, out[i].TargetType, out[i].TargetID, out[i].UnitKind)
		out[i].ScopeLabel = s.scopeLabel(ctx, s.db, out[i].Scope, out[i].ScopeEntityID)
	}
	if out == nil {
		out = []GrantView{}
	}
	return out, nil
}

type labelQ interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scopeLabel 은 적용 범위의 사람이 읽는 이름이다(화면·감사).
func (s *Service) scopeLabel(ctx context.Context, q labelQ, scope string, entityID int64) string {
	switch scope {
	case contract.ScopeGroup:
		return "그룹 전체"
	case contract.ScopeEntity:
		var name string
		if q.QueryRowContext(ctx, `SELECT name FROM entities WHERE id = ?`, entityID).Scan(&name) == nil {
			return name + " 법인"
		}
		return "지정 법인"
	}
	return "소속 법인 전체"
}

var unitKindLabel = map[string]string{"company": "회사", "division": "본부", "department": "부서", "team": "팀"}

// targetLabel 은 부여 대상의 사람이 읽는 이름이다(화면·감사).
func (s *Service) targetLabel(ctx context.Context, q labelQ, ttype string, id int64, kind string) string {
	var name string
	switch ttype {
	case "user":
		if q.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, id).Scan(&name) == nil {
			return name
		}
	case "unit":
		if q.QueryRowContext(ctx, `SELECT name FROM org_unit_spans WHERE unit_id = ? ORDER BY valid_from DESC LIMIT 1`, id).Scan(&name) == nil {
			return name
		}
	case "rank":
		if q.QueryRowContext(ctx, `SELECT name FROM org_ranks WHERE id = ?`, id).Scan(&name) == nil {
			return name
		}
	case "unit_head":
		return unitKindLabel[kind] + " 조직장"
	}
	return fmt.Sprintf("%s #%d", ttype, id)
}

// GrantPreview 는 부여를 하면 누가 영향을 받는지 미리 보여 준다(적용하지 않는다).
type GrantPreview struct {
	AffectedUsers int      `json:"affectedUsers"`
	NewlyGained   int      `json:"newlyGained"` // 새로 권한이 생기는 사람 수
	Sample        []string `json:"sample"`      // 새로 권한이 생기는 사람 이름(최대 10명)
	RiskPerms     []string `json:"riskPermissions"`
	NeedsApproval bool     `json:"needsApproval"` // 다른 관리자의 승인이 필요한지
}

var errPreviewDone = errors.New("preview done")

// PreviewGrant 는 트랜잭션 안에서 가상으로 부여해 계산한 뒤 되돌린다.
func (s *Service) PreviewGrant(ctx context.Context, p contract.Principal, roleID int64, in GrantInput) (GrantPreview, error) {
	if err := s.authorize(ctx, p); err != nil {
		return GrantPreview{}, err
	}
	var out GrantPreview
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := in.validate(ctx, tx); err != nil {
			return err
		}
		risky, err := s.roleHasRisk(ctx, tx, roleID)
		if err != nil {
			return err
		}
		if sc, _ := in.scope(); sc == contract.ScopeGroup {
			risky = append(risky, "그룹 전체 범위")
		}
		out.RiskPerms = risky
		if len(risky) > 0 {
			solo, err := s.soloHolder(ctx, tx, p)
			if err != nil {
				return err
			}
			out.NeedsApproval = !solo
		}
		before, err := userPermCounts(ctx, tx)
		if err != nil {
			return err
		}
		tid, kind, incl := in.key()
		pScope, pEntity := in.scope()
		gid, err := dbutil.InsertID(ctx, tx, `INSERT INTO role_grants (role_id, target_type, target_id, unit_kind, include_descendants, source, created_by, created_at, scope, scope_entity_id) VALUES (?,?,?,?,?,'manual',?,?,?,?)`,
			roleID, in.TargetType, tid, kind, incl, p.UserID, s.stamp(), pScope, pEntity)
		if err != nil {
			if dbutil.IsUnique(err) {
				return serr("이미 같은 대상에게 이 역할이 부여되어 있습니다")
			}
			return err
		}
		if err := Recompute(ctx, tx, s.today()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT e.user_id, COALESCE(u.name,'') FROM user_effective_permissions e LEFT JOIN users u ON u.id = e.user_id WHERE e.grant_id = ? ORDER BY e.user_id`, gid)
		if err != nil {
			return err
		}
		type pu struct {
			id   int64
			name string
		}
		var us []pu
		for rows.Next() {
			var x pu
			if err := rows.Scan(&x.id, &x.name); err != nil {
				rows.Close()
				return err
			}
			us = append(us, x)
		}
		rows.Close()
		after, err := userPermCounts(ctx, tx)
		if err != nil {
			return err
		}
		out.AffectedUsers = len(us)
		out.Sample = []string{}
		for _, u := range us {
			if after[u.id] > before[u.id] {
				out.NewlyGained++
				if len(out.Sample) < 10 {
					out.Sample = append(out.Sample, u.name)
				}
			}
		}
		return errPreviewDone // 되돌린다
	})
	if errors.Is(err, errPreviewDone) {
		err = nil
	}
	return out, err
}

func userPermCounts(ctx context.Context, tx contract.Tx) (map[int64]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id, COUNT(DISTINCT permission) FROM user_effective_permissions GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---- 마지막 관리자 보호 ----

// guardedPerms 는 활성·재직 보유자가 0명이 되면 안 되는 권한이다(역할·부여 관리, 로그인 설정).
var guardedPerms = []contract.Permission{contract.PermRoleManage, contract.PermAuthManage}

// HolderCount 는 오늘 활성·재직(조직도에 등록된 사람은 오늘 재직 구간이 있어야 함)인 권한 보유자 수다.
func HolderCount(ctx context.Context, q labelQ, perm contract.Permission, today contract.Day) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users u ON u.id = e.user_id AND u.active = 1
		WHERE e.permission = ?2 AND (
		  NOT EXISTS (SELECT 1 FROM org_person_spans s WHERE s.user_id = u.id)
		  OR EXISTS (SELECT 1 FROM org_person_spans s WHERE s.user_id = u.id AND s.left_flag = 0 AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1)))`,
		string(today), string(perm)).Scan(&n)
	return n, err
}

// Guard 는 fn 이 바꾼 뒤 활성·재직 role:manage·auth:manage 보유자가 이전에 있었는데 0명이 되면 오류를 돌려 트랜잭션을 되돌리게 한다.
// 다른 패키지(사용자 비활성·역할 변경)도 같은 보호를 쓰도록 공개한다.
func Guard(ctx context.Context, tx Execer, today contract.Day, fn func() error) error {
	before := map[contract.Permission]int{}
	for _, p := range guardedPerms {
		n, err := HolderCount(ctx, tx, p, today)
		if err != nil {
			return err
		}
		before[p] = n
	}
	if err := fn(); err != nil {
		return err
	}
	for _, p := range guardedPerms {
		after, err := HolderCount(ctx, tx, p, today)
		if err != nil {
			return err
		}
		if before[p] > 0 && after == 0 {
			return serr("이 변경으로 %s 권한을 가진 활성 사용자가 한 명도 남지 않게 됩니다. 다른 사람에게 먼저 권한을 주세요", guardLabel(p))
		}
	}
	return nil
}

func guardLabel(p contract.Permission) string {
	if p == contract.PermAuthManage {
		return "로그인 설정(auth:manage)"
	}
	return "역할·부여 관리(role:manage)"
}

func (s *Service) guardHolders(ctx context.Context, tx contract.Tx, fn func() error) error {
	return Guard(ctx, tx, s.today(), fn)
}

// soloHolder 는 요청자가 활성·재직 role:manage 보유자 가운데 유일한지다(2인 확인을 할 수 없는 경우).
func (s *Service) soloHolder(ctx context.Context, tx contract.Tx, p contract.Principal) (bool, error) {
	n, err := HolderCount(ctx, tx, contract.PermRoleManage, s.today())
	if err != nil {
		return false, err
	}
	return n <= 1, nil
}

// ---- 위험 권한 2인 확인 ----

type reqPayload struct {
	RoleID      int64       `json:"roleId"`
	Grant       *GrantInput `json:"grant,omitempty"`
	Version     int64       `json:"version,omitempty"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
	Permissions []string    `json:"permissions,omitempty"`
}

// Request 는 2인 확인 요청 한 건이다.
type Request struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"` // grant | role_update
	Summary     string   `json:"summary"`
	Reason      string   `json:"reason"`
	RiskPerms   []string `json:"riskPermissions"`
	RequestedBy int64    `json:"requestedBy"`
	Requester   string   `json:"requester"`
	RequestedAt string   `json:"requestedAt"`
	ExpiresAt   string   `json:"expiresAt"`
	Status      string   `json:"status"`
	DecidedBy   int64    `json:"decidedBy,omitempty"`
}

func (s *Service) summarize(ctx context.Context, q labelQ, kind string, pl reqPayload) string {
	var role string
	_ = q.QueryRowContext(ctx, `SELECT name FROM roles WHERE id = ?`, pl.RoleID).Scan(&role)
	switch kind {
	case "grant":
		if pl.Grant != nil {
			tid, k, _ := pl.Grant.key()
			return fmt.Sprintf("역할 '%s' 을(를) %s 에게 부여", role, s.targetLabel(ctx, q, pl.Grant.TargetType, tid, k))
		}
	case "role_update":
		return fmt.Sprintf("역할 '%s' 의 권한 변경(위험 권한 추가)", role)
	}
	return kind
}

func (s *Service) createRequest(ctx context.Context, tx contract.Tx, p contract.Principal, kind string, pl reqPayload, reason string, risky []string) (Request, error) {
	b, _ := json.Marshal(pl)
	now := s.now().UTC()
	exp := now.Add(requestTTL)
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO role_change_requests (kind, payload_json, reason, requested_by, requested_at, status, expires_at) VALUES (?,?,?,?,?,'pending',?)`,
		kind, string(b), strings.TrimSpace(reason), p.UserID, now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
	if err != nil {
		return Request{}, err
	}
	sum := s.summarize(ctx, tx, kind, pl)
	if err := s.record(ctx, tx, p, "role.request", audit.TargetRole, fmt.Sprint(pl.RoleID),
		[]audit.DiffItem{{Key: "request_kind", New: kind}, {Key: "request_summary", New: sum}, {Key: "risk_permissions", New: strings.Join(risky, ",")}, {Key: "request_id", New: fmt.Sprint(id)}}, ""); err != nil {
		return Request{}, err
	}
	return Request{ID: id, Kind: kind, Summary: sum, Reason: strings.TrimSpace(reason), RiskPerms: risky, RequestedBy: p.UserID, RequestedAt: now.Format(time.RFC3339Nano), ExpiresAt: exp.Format(time.RFC3339Nano), Status: "pending"}, nil
}

// ListRequests 는 요청 목록(대기 먼저, 최근 50건)이다. 만료된 대기 요청은 상태를 expired 로 바꿔 보인다.
func (s *Service) ListRequests(ctx context.Context, p contract.Principal) ([]Request, error) {
	if err := s.authorize(ctx, p); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE role_change_requests SET status = 'expired' WHERE status = 'pending' AND expires_at <= ?`, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT q.id, q.kind, q.payload_json, q.reason, q.requested_by, COALESCE(u.name,''), q.requested_at, q.expires_at, q.status, COALESCE(q.decided_by,0)
		FROM role_change_requests q LEFT JOIN users u ON u.id = q.requested_by
		ORDER BY CASE q.status WHEN 'pending' THEN 0 ELSE 1 END, q.id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	var out []Request
	var payloads []reqPayload
	for rows.Next() {
		var r Request
		var pj string
		if err := rows.Scan(&r.ID, &r.Kind, &pj, &r.Reason, &r.RequestedBy, &r.Requester, &r.RequestedAt, &r.ExpiresAt, &r.Status, &r.DecidedBy); err != nil {
			rows.Close()
			return nil, err
		}
		var pl reqPayload
		_ = json.Unmarshal([]byte(pj), &pl)
		out = append(out, r)
		payloads = append(payloads, pl)
	}
	rows.Close()
	for i := range out {
		out[i].Summary = s.summarize(ctx, s.db, out[i].Kind, payloads[i])
		perms := payloads[i].Permissions
		if out[i].Kind == "grant" {
			if r, err := loadRole(ctx, s.db, payloads[i].RoleID); err == nil {
				perms = r.Permissions
			}
		}
		out[i].RiskPerms = riskOf(perms)
	}
	if out == nil {
		out = []Request{}
	}
	return out, nil
}

func (s *Service) loadPending(ctx context.Context, tx contract.Tx, id int64) (Request, reqPayload, error) {
	var r Request
	var pj string
	err := tx.QueryRowContext(ctx, `SELECT id, kind, payload_json, reason, requested_by, requested_at, expires_at, status FROM role_change_requests WHERE id = ?`, id).
		Scan(&r.ID, &r.Kind, &pj, &r.Reason, &r.RequestedBy, &r.RequestedAt, &r.ExpiresAt, &r.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return r, reqPayload{}, contract.ErrNotFound
	}
	if err != nil {
		return r, reqPayload{}, err
	}
	var pl reqPayload
	if err := json.Unmarshal([]byte(pj), &pl); err != nil {
		return r, pl, err
	}
	if r.Status == "pending" {
		exp, _ := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		if !s.now().Before(exp) {
			if _, err := tx.ExecContext(ctx, `UPDATE role_change_requests SET status = 'expired' WHERE id = ?`, id); err != nil {
				return r, pl, err
			}
			r.Status = "expired"
		}
	}
	return r, pl, nil
}

// Approve 는 다른 role:manage 보유자가 요청을 승인해 적용한다(본인 요청은 승인할 수 없다, 24시간 안에).
func (s *Service) Approve(ctx context.Context, p contract.Principal, id int64) error {
	if err := s.authorize(ctx, p); err != nil {
		return err
	}
	var expired bool
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		r, pl, err := s.loadPending(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Status == "expired" {
			expired = true
			return nil // 상태 변경(expired)을 커밋한다
		}
		if r.Status != "pending" {
			return serr("이미 처리된 요청입니다")
		}
		if s.sod != nil { // 위험 역할 요청자 본인의 승인(role_request_approve, 잠금 차단)
			if _, err := s.sod.Evaluate(ctx, tx, sod.Check{Rule: sod.RuleRoleRequestApprove, Actor: p, Subject: sod.Ref{Type: "role_change_request", ID: id}, Other: r.RequestedBy}); err != nil {
				return err
			}
		}
		if r.RequestedBy == p.UserID {
			return serr("본인이 올린 요청은 승인할 수 없습니다. 다른 관리자가 승인해야 합니다")
		}
		if err := s.guardHolders(ctx, tx, func() error {
			switch r.Kind {
			case "grant":
				if pl.Grant == nil {
					return serr("요청 내용이 올바르지 않습니다")
				}
				if err := pl.Grant.validate(ctx, tx); err != nil {
					return err
				}
				cur, err := loadRole(ctx, tx, pl.RoleID)
				if err != nil {
					return err
				}
				if pl.Version != 0 && cur.Version != pl.Version {
					return serr("그 사이 역할이 바뀌어 요청을 적용할 수 없습니다. 반려하고 다시 요청하세요")
				}
				requester := contract.Principal{UserID: r.RequestedBy, Channel: contract.ChannelWeb}
				_, _, err = s.applyGrant(ctx, tx, requester, pl.RoleID, *pl.Grant, false, p.UserID)
				return err
			case "role_update":
				cur, err := loadRole(ctx, tx, pl.RoleID)
				if err != nil {
					return err
				}
				if cur.Version != pl.Version {
					return serr("그 사이 역할이 바뀌어 요청을 적용할 수 없습니다. 반려하고 다시 요청하세요")
				}
				added, removed := diffSets(cur.Permissions, pl.Permissions)
				requester := contract.Principal{UserID: r.RequestedBy, Channel: contract.ChannelWeb}
				return s.applyRoleUpdate(ctx, tx, requester, cur, pl.Name, pl.Description, pl.Permissions, added, removed, false, p.UserID)
			}
			return serr("알 수 없는 요청 종류입니다")
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE role_change_requests SET status = 'approved', decided_by = ?, decided_at = ? WHERE id = ?`, p.UserID, s.stamp(), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "role.approve", audit.TargetRole, fmt.Sprint(pl.RoleID),
			[]audit.DiffItem{{Key: "request_kind", New: r.Kind}, {Key: "request_summary", New: s.summarize(ctx, tx, r.Kind, pl)}, {Key: "request_id", New: fmt.Sprint(id)}, {Key: "requested_by", New: fmt.Sprint(r.RequestedBy)}}, "")
	})
	if err == nil && expired {
		return serr("요청이 24시간이 지나 만료되었습니다. 다시 요청하세요")
	}
	if c, ok := sod.IsBlocked(err); ok && c.Rule == sod.RuleRoleRequestApprove {
		s.noteBlocked(ctx, err) // 차단 기록은 남기되 화면에는 기존과 같은 안내(409)를 보인다
		return serr("본인이 올린 요청은 승인할 수 없습니다. 다른 관리자가 승인해야 합니다")
	}
	return s.noteBlocked(ctx, err)
}

// Reject 는 요청을 반려한다(사유 필수).
func (s *Service) Reject(ctx context.Context, p contract.Principal, id int64, reason string) error {
	if err := s.authorize(ctx, p); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return verr("반려 사유를 입력하세요")
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		r, pl, err := s.loadPending(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Status != "pending" {
			return serr("이미 처리된 요청이거나 만료되었습니다")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE role_change_requests SET status = 'rejected', decided_by = ?, decided_at = ?, reason = reason || ' / 반려: ' || ? WHERE id = ?`, p.UserID, s.stamp(), strings.TrimSpace(reason), id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "role.reject", audit.TargetRole, fmt.Sprint(pl.RoleID),
			[]audit.DiffItem{{Key: "request_kind", New: r.Kind}, {Key: "request_id", New: fmt.Sprint(id)}, {Key: "grant_reason", New: strings.TrimSpace(reason)}}, "")
	})
}

// ---- 권한 출처 ----

// AccessSource 는 권한 하나가 어디서 왔는지(사람이 읽는 형태)다.
type AccessSource struct {
	Permission string `json:"permission"`
	RoleID     int64  `json:"roleId"`
	RoleName   string `json:"roleName"`
	GrantID    int64  `json:"grantId"`
	Via        string `json:"via"` // 사람 직접 | 조직 이름 | 직위 이름 | 조직장
	TargetType string `json:"targetType"`
}

// UserAccess 는 사용자의 실효 권한과 출처다.
type UserAccess struct {
	UserID      int64          `json:"userId"`
	Name        string         `json:"name"`
	Roles       []string       `json:"roles"`
	Permissions []string       `json:"permissions"`
	Sources     []AccessSource `json:"sources"`
}

func (s *Service) userAccess(ctx context.Context, q interface {
	Querier
	labelQ
}, userID int64) (UserAccess, error) {
	var ua UserAccess
	ua.UserID = userID
	if err := q.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, userID).Scan(&ua.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ua, contract.ErrNotFound
		}
		return ua, err
	}
	srcs, err := Sources(ctx, q, userID)
	if err != nil {
		return ua, err
	}
	perms, roles := map[string]bool{}, map[string]bool{}
	ua.Sources = []AccessSource{}
	for _, x := range srcs {
		var tid int64
		var kind string
		_ = q.QueryRowContext(ctx, `SELECT unit_kind FROM role_grants WHERE id = ?`, x.GrantID).Scan(&kind)
		tid = x.TargetID
		via := "사람 직접"
		if x.TargetType != "user" {
			via = s.targetLabel(ctx, q, x.TargetType, tid, kind)
			if x.TargetType == "unit" {
				via += " 소속"
			} else if x.TargetType == "rank" {
				via += " 직위"
			}
		}
		perms[x.Permission], roles[x.RoleName] = true, true
		ua.Sources = append(ua.Sources, AccessSource{Permission: x.Permission, RoleID: x.RoleID, RoleName: x.RoleName, GrantID: x.GrantID, Via: via, TargetType: x.TargetType})
	}
	for p := range perms {
		ua.Permissions = append(ua.Permissions, p)
	}
	for r := range roles {
		ua.Roles = append(ua.Roles, r)
	}
	sort.Strings(ua.Permissions)
	sort.Strings(ua.Roles)
	if ua.Permissions == nil {
		ua.Permissions = []string{}
	}
	if ua.Roles == nil {
		ua.Roles = []string{}
	}
	return ua, nil
}

// UserAccess 는 사용자 한 명의 권한 출처다(role:manage).
func (s *Service) UserAccess(ctx context.Context, p contract.Principal, userID int64) (UserAccess, error) {
	if err := s.authorize(ctx, p); err != nil {
		return UserAccess{}, err
	}
	return s.userAccess(ctx, s.db, userID)
}

// MyAccess 는 본인의 권한 출처다(로그인한 누구나, 제한 세션 제외).
func (s *Service) MyAccess(ctx context.Context, p contract.Principal) (UserAccess, error) {
	if p.UserID == 0 {
		return UserAccess{}, contract.ErrUnauthenticated
	}
	return s.userAccess(ctx, s.db, p.UserID)
}

// ---- 권한 분리 점검 ----

// SepRule 은 한 사람이 함께 가지면 안 좋은 권한 쌍이다(막지는 않고 점검 화면에 보인다).
type SepRule struct {
	A      contract.Permission `json:"a"`
	B      contract.Permission `json:"b"`
	Reason string              `json:"reason"`
}

// SeparationRules 는 초기 충돌 쌍이다(코드에 있고 바꾸려면 배포가 필요하다).
var SeparationRules = []SepRule{
	{contract.PermPurchaseCreate, contract.PermApprovalDecide, "요청자가 결재도 하는 구성입니다(자기 결재는 코드가 막지만 결재 후보 풀이 겹칩니다)"},
	{contract.PermLedgerWrite, contract.PermPaymentWrite, "전표를 쓰는 사람이 지급도 합니다"},
	{contract.PermPaymentWrite, contract.PermAuditVerify, "지급 처리자가 감사 검증도 합니다"},
	{contract.PermRoleManage, contract.PermAuditVerify, "권한을 관리하는 사람이 감사 검증도 합니다"},
	{contract.PermCardManage, contract.PermCardConfirm, "카드를 관리하는 사람이 소명도 확인합니다"},
	{contract.PermInventoryManage, contract.PermInventoryClose, "재고를 다루는 사람이 월 마감도 합니다"},
}

// SepFinding 은 규칙 하나에 걸린 사람들이다.
type SepFinding struct {
	Rule  SepRule   `json:"rule"`
	Users []SepUser `json:"users"`
	Count int       `json:"count"`
}

// SepUser 는 규칙에 걸린 사람과 두 권한의 출처 역할이다.
type SepUser struct {
	UserID int64    `json:"userId"`
	Name   string   `json:"name"`
	RolesA []string `json:"rolesA"`
	RolesB []string `json:"rolesB"`
}

// Separation 은 권한 분리 점검 결과다(활성 사용자만).
func (s *Service) Separation(ctx context.Context, p contract.Principal) ([]SepFinding, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermRoleManage); err != nil {
		return nil, err
	}
	out := []SepFinding{}
	for _, rule := range SeparationRules {
		rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.name,
			(SELECT string_agg(n, ',') FROM (SELECT DISTINCT r.name AS n FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id WHERE e.user_id = u.id AND e.permission = ?1 ORDER BY r.name)),
			(SELECT string_agg(n, ',') FROM (SELECT DISTINCT r.name AS n FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id WHERE e.user_id = u.id AND e.permission = ?2 ORDER BY r.name))
			FROM users u WHERE u.active = 1
			  AND EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = ?1)
			  AND EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = ?2)
			ORDER BY u.id`, string(rule.A), string(rule.B))
		if err != nil {
			return nil, err
		}
		f := SepFinding{Rule: rule, Users: []SepUser{}}
		for rows.Next() {
			var u SepUser
			var a, b sql.NullString
			if err := rows.Scan(&u.UserID, &u.Name, &a, &b); err != nil {
				rows.Close()
				return nil, err
			}
			u.RolesA, u.RolesB = splitNames(a.String), splitNames(b.String)
			f.Users = append(f.Users, u)
		}
		rows.Close()
		f.Count = len(f.Users)
		out = append(out, f)
	}
	return out, nil
}

func splitNames(s string) []string {
	if s == "" {
		return []string{}
	}
	out := strings.Split(s, ",")
	sort.Strings(out)
	return out
}

// ---- 일일 대조·복구 ----

// Reconcile 은 캐시를 전체 재계산과 대조하고 어긋나면 바로잡아 감사(role.recompute)를 남긴다. 바뀐 행 수를 돌려준다.
// force 가 참이면(서버 명령) 어긋남이 없어도 감사를 남긴다.
func (s *Service) Reconcile(ctx context.Context, actor contract.Principal, force bool) (int, error) {
	changed := 0
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		n, err := RecomputeAll(ctx, tx, s.today())
		if err != nil {
			return err
		}
		changed = n
		if n == 0 && !force {
			return nil
		}
		return s.record(ctx, tx, actor, "role.recompute", audit.TargetRole, "all", []audit.DiffItem{{Key: "rows_changed", New: fmt.Sprint(n)}}, "")
	})
	return changed, err
}

// Run 은 서버가 켜져 있는 동안 날짜가 바뀔 때마다(기동 때 한 번 포함) 캐시를 대조한다.
// 조직 변경은 조직 서비스가 같은 트랜잭션에서 다시 계산하므로, 이 작업은 어긋남을 잡는 안전망이다.
func (s *Service) Run(ctx context.Context) {
	var last contract.Day
	tick := func() {
		d := s.today()
		if d == last {
			return
		}
		if n, err := s.Reconcile(ctx, systemActor, false); err != nil {
			fmt.Printf("access: 실효 권한 대조 실패: %v\n", err)
			return
		} else if n > 0 {
			fmt.Printf("access: 실효 권한 캐시가 어긋나 있어 바로잡았습니다(%d행)\n", n)
		}
		last = d
	}
	tick()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
