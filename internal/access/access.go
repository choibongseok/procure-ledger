// Package access 는 사용자 정의 역할(B33)의 권한 계산이다: 역할 표·부여·실효 권한 캐시.
//
// 불변식
//   - 권한 판정은 contract.Principal.Can 한 곳이다. 이 패키지는 판정에 쓸 실효 권한 집합(PermsOf)과 그 출처(Sources)를 계산한다.
//   - 기본 템플릿 5개의 권한은 코드(contract.RolePermissions)가 정본이고 서버 기동 때 SyncTemplates 가 DB 에 맞춘다. DB 에서 고치지 않는다.
//   - 실효 권한 캐시(user_effective_permissions)는 변경 시점에 같은 트랜잭션에서 맞춘다. 사용자 직접 부여는 마이그레이션의 트리거가,
//     그 밖은 RecomputeAll 이 한다. 거부 규칙은 없고 부여의 합집합이다.
package access

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// Querier 는 읽기에 필요한 최소 인터페이스다(*sql.DB·contract.Tx 가 만족한다).
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Execer 는 쓰기에 필요한 최소 인터페이스다.
type Execer interface {
	Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PermsOf 는 사용자의 실효 권한 집합이다(항상 non-nil, 권한이 없으면 빈 집합). 활성 여부는 보지 않는다(호출하는 쪽이 본다).
func PermsOf(ctx context.Context, q Querier, userID int64) (contract.PermSet, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT permission FROM user_effective_permissions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := contract.NewPermSet()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[contract.Permission(p)] = struct{}{}
	}
	return out, rows.Err()
}

// EntityPermsOf 는 사용자의 권한별 효력 법인 집합이다(B44 9절). 부여의 범위를 읽을 때 푼다: own 은 own(소속·겸직 법인) 목록, entity 는 지정 법인, group 은 전체 법인.
// 같은 권한을 여러 부여로 가지면 합집합이다. own 이 비어 있는(조직 미배치) 사람의 own 부여는 효력이 없다.
func EntityPermsOf(ctx context.Context, q Querier, userID int64, own []int64) (map[contract.Permission]contract.EntitySet, error) {
	rows, err := q.QueryContext(ctx, `SELECT permission, scope, entity_id FROM user_effective_permissions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[contract.Permission]contract.EntitySet{}
	for rows.Next() {
		var perm, scope string
		var eid int64
		if err := rows.Scan(&perm, &scope, &eid); err != nil {
			return nil, err
		}
		var set contract.EntitySet
		switch scope {
		case contract.ScopeGroup:
			set = contract.EntitySet{All: true}
		case contract.ScopeEntity:
			set = contract.NewEntitySet(eid)
		default:
			set = contract.NewEntitySet(own...)
		}
		k := contract.Permission(perm)
		out[k] = out[k].Union(set)
	}
	return out, rows.Err()
}

// RoleNamesOf 는 사용자에게 권한을 준 역할 이름들이다(표시용, 정렬).
func RoleNamesOf(ctx context.Context, q Querier, userID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT r.name FROM role_grants g JOIN roles r ON r.id = g.role_id
		WHERE g.target_type = 'user' AND g.target_id = ? AND r.deleted_at IS NULL ORDER BY r.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Source 는 권한 하나가 어디서 왔는지다(권한 → 역할 → 부여).
type Source struct {
	Permission string `json:"permission"`
	RoleID     int64  `json:"roleId"`
	RoleName   string `json:"roleName"`
	GrantID    int64  `json:"grantId"`
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
}

// Sources 는 사용자의 실효 권한의 출처 목록이다(권한 이름·역할 이름 순).
func Sources(ctx context.Context, q Querier, userID int64) ([]Source, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.permission, e.role_id, r.name, e.grant_id, g.target_type, g.target_id
		FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id JOIN role_grants g ON g.id = e.grant_id
		WHERE e.user_id = ? ORDER BY e.permission, r.name, e.grant_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.Permission, &s.RoleID, &s.RoleName, &s.GrantID, &s.TargetType, &s.TargetID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SyncResult 는 템플릿 동기화가 바꾼 내용이다.
type SyncResult struct {
	Added, Removed int
	Unknown        []string // 코드에 없는 권한이라 제거한 권한(역할·권한)
}

// SyncTemplates 는 기본 템플릿 5개의 권한을 코드(contract.RolePermissions)와 같게 맞춘다. 멱등이다. 또 코드에 없는 권한 문자열이 든 역할
// 권한은 모두(사용자 정의 역할 포함) 제거한다(코드에서 권한이 삭제된 경우, E8). 권한이 바뀌면 사용자 직접 부여의 캐시는 트리거가 맞춘다.
func SyncTemplates(ctx context.Context, db *sql.DB) (SyncResult, error) {
	var res SyncResult
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	known := map[string]bool{}
	for _, p := range contract.AllPermissions {
		known[string(p)] = true
	}
	for _, role := range contract.Roles {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE key = ? AND kind = 'template'`, string(role)).Scan(&id)
		if err == sql.ErrNoRows {
			now := time.Now().UTC().Format(time.RFC3339)
			var ierr error
			id, ierr = dbutil.InsertID(ctx, tx, `INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES (?,?,'template',?,?)`, string(role), string(role), now, now)
			if ierr != nil {
				return res, ierr
			}
		} else if err != nil {
			return res, err
		}
		have := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT permission FROM role_permissions WHERE role_id = ?`, id)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return res, err
			}
			have[p] = true
		}
		rows.Close()
		want := map[string]bool{}
		for _, p := range contract.RolePermissions[role] {
			want[string(p)] = true
		}
		for p := range want {
			if !have[p] {
				if _, err := tx.ExecContext(ctx, `INSERT INTO role_permissions (role_id, permission) VALUES (?,?)`, id, p); err != nil {
					return res, err
				}
				res.Added++
			}
		}
		for p := range have {
			if !want[p] {
				if _, err := tx.ExecContext(ctx, `DELETE FROM role_permissions WHERE role_id = ? AND permission = ?`, id, p); err != nil {
					return res, err
				}
				res.Removed++
			}
		}
	}
	// 코드에 없는 권한(템플릿 외 역할 포함) 제거.
	rows, err := tx.QueryContext(ctx, `SELECT r.key, rp.permission FROM role_permissions rp JOIN roles r ON r.id = rp.role_id`)
	if err != nil {
		return res, err
	}
	type rp struct{ key, perm string }
	var bad []rp
	for rows.Next() {
		var x rp
		if err := rows.Scan(&x.key, &x.perm); err != nil {
			rows.Close()
			return res, err
		}
		if !known[x.perm] {
			bad = append(bad, x)
		}
	}
	rows.Close()
	for _, x := range bad {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_permissions WHERE permission = ? AND role_id = (SELECT id FROM roles WHERE key = ?)`, x.perm, x.key); err != nil {
			return res, err
		}
		res.Unknown = append(res.Unknown, x.key+":"+x.perm)
	}
	return res, tx.Commit()
}

// Recompute 는 실효 권한 캐시를 부여·역할 권한·오늘(KST, YYYY-MM-DD) 기준 조직도에서 처음부터 다시 계산한다(호출하는 쪽의 트랜잭션 안에서).
// 부여 유형: user(직접), unit(구성원, 하위 포함 선택, 주소속·겸직 모두), rank(직위), unit_head(조직 종류별 조직장: 직무대행 제외).
// 조직도에 등록되지 않은 사람에게는 user 부여만 적용된다. 퇴사·입사 전 구간은 조직도 기반 부여에서 빠진다.
func Recompute(ctx context.Context, tx Execer, today contract.Day) error {
	return RecomputeAs(ctx, tx, today, false)
}

// RecomputeAs 는 Recompute 와 같되 includeInactive 가 참이면 비활성 계정도 계산에 넣는다. 발령이 위험 권한을 새로 만드는지 날짜별로
// 미리 가늠할 때(아직 활성이 아닌 재입사자 포함) 쓰는 계산이다. 계산 뒤에는 Recompute(오늘)로 캐시를 되돌린다.
func RecomputeAs(ctx context.Context, tx Execer, today contract.Day, includeInactive bool) error {
	flag := 0
	if includeInactive {
		flag = 1
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_effective_permissions`); err != nil {
		return err
	}
	d := string(today)
	// 오늘 재직 중인 사람(조직도 구간), 오늘 유효한 조직 구간.
	_, err := tx.ExecContext(ctx, `
WITH RECURSIVE
 active_people AS (
   SELECT user_id, rank_id, primary_unit_id FROM org_person_spans
   WHERE left_flag = 0 AND valid_from <= ?1 AND (valid_to IS NULL OR valid_to > ?1)
 ),
 active_units AS (
   SELECT unit_id, parent_id, kind, head_user_id, head_acting FROM org_unit_spans
   WHERE valid_from <= ?1 AND (valid_to IS NULL OR valid_to > ?1)
 ),
 tree(root, id) AS (
   SELECT g.target_id, g.target_id FROM role_grants g WHERE g.target_type = 'unit' AND g.include_descendants = 1
   UNION
   SELECT t.root, u.unit_id FROM tree t JOIN active_units u ON u.parent_id = t.id
 ),
 members(grant_id, user_id) AS (
   SELECT g.id, g.target_id FROM role_grants g WHERE g.target_type = 'user'
   UNION
   SELECT g.id, p.user_id FROM role_grants g JOIN active_people p ON p.primary_unit_id = g.target_id
     WHERE g.target_type = 'unit' AND g.include_descendants = 0
   UNION
   SELECT g.id, c.user_id FROM role_grants g JOIN org_concurrent_spans c ON c.unit_id = g.target_id
     AND c.valid_from <= ?1 AND (c.valid_to IS NULL OR c.valid_to > ?1)
     JOIN active_people p ON p.user_id = c.user_id
     WHERE g.target_type = 'unit' AND g.include_descendants = 0
   UNION
   SELECT g.id, p.user_id FROM role_grants g JOIN tree t ON t.root = g.target_id JOIN active_people p ON p.primary_unit_id = t.id
     WHERE g.target_type = 'unit' AND g.include_descendants = 1
   UNION
   SELECT g.id, c.user_id FROM role_grants g JOIN tree t ON t.root = g.target_id JOIN org_concurrent_spans c ON c.unit_id = t.id
     AND c.valid_from <= ?1 AND (c.valid_to IS NULL OR c.valid_to > ?1)
     JOIN active_people p ON p.user_id = c.user_id
     WHERE g.target_type = 'unit' AND g.include_descendants = 1
   UNION
   SELECT g.id, p.user_id FROM role_grants g JOIN active_people p ON p.rank_id = g.target_id WHERE g.target_type = 'rank'
   UNION
   SELECT g.id, u.head_user_id FROM role_grants g JOIN active_units u ON u.kind = g.unit_kind AND u.head_acting = 0 AND u.head_user_id <> 0
     JOIN active_people p ON p.user_id = u.head_user_id
     WHERE g.target_type = 'unit_head'
 )
INSERT INTO user_effective_permissions (user_id, permission, role_id, grant_id, scope, entity_id)
SELECT m.user_id, rp.permission, g.role_id, g.id, g.scope, g.scope_entity_id
FROM members m JOIN role_grants g ON g.id = m.grant_id
JOIN roles r ON r.id = g.role_id AND r.deleted_at IS NULL
JOIN role_permissions rp ON rp.role_id = g.role_id
JOIN users uu ON uu.id = m.user_id AND (uu.active = 1 OR ?2 = 1)
WHERE NOT EXISTS (SELECT 1 FROM org_person_spans s WHERE s.user_id = m.user_id AND s.left_flag = 1 AND s.valid_from <= ?1 AND (s.valid_to IS NULL OR s.valid_to > ?1))
ON CONFLICT DO NOTHING`, d, flag)
	return err
}

// RecomputeAll 은 Recompute 한 뒤 이전 캐시와 달라진 행 수(삭제+삽입)를 돌려준다(일일 대조·시험용: 이전 캐시를 통째로 읽으므로 변경 때마다 부르지 않는다).
func RecomputeAll(ctx context.Context, tx Execer, today contract.Day) (int, error) {
	snap, err := snapshot(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := Recompute(ctx, tx, today); err != nil {
		return 0, err
	}
	after, err := snapshot(ctx, tx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for k := range snap {
		if !after[k] {
			changed++
		}
	}
	for k := range after {
		if !snap[k] {
			changed++
		}
	}
	return changed, nil
}

func snapshot(ctx context.Context, q Querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT user_id, permission, role_id, grant_id FROM user_effective_permissions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u, r, g int64
		var p string
		if err := rows.Scan(&u, &p, &r, &g); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d|%s|%d|%d", u, p, r, g)] = true
	}
	return out, rows.Err()
}

// CacheDiffers 는 캐시가 전체 재계산 결과와 다른지 본다(일일 대조·시험). 다르면 true.
func CacheDiffers(ctx context.Context, db *sql.DB, today contract.Day) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() // 읽기 전용 비교: 항상 되돌린다
	n, err := RecomputeAll(ctx, tx, today)
	return n > 0, err
}

// SortedStrings 는 권한 집합을 문자열 목록으로 돌려준다(시험·직렬화용).
func SortedStrings(s contract.PermSet) []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// HasPerm 은 사용자가 그 권한을 가졌는지다(활성 여부는 보지 않는다).
func HasPerm(ctx context.Context, q labelQ, userID int64, perm contract.Permission) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = ? AND permission = ?`, userID, string(perm)).Scan(&n)
	return n > 0, err
}

// RoleHasPermission 은 역할(키)에 그 권한이 들어 있는지다(템플릿·사용자 정의 모두). 없는 역할은 false.
func RoleHasPermission(ctx context.Context, q labelQ, roleKey string, perm contract.Permission) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE r.key = ? AND r.deleted_at IS NULL AND rp.permission = ?`, roleKey, string(perm)).Scan(&n)
	return n > 0, err
}

// RoleName 은 역할 키의 이름이다(없으면 키 그대로).
func RoleName(ctx context.Context, q labelQ, roleKey string) string {
	var name string
	if q.QueryRowContext(ctx, `SELECT name FROM roles WHERE key = ? AND deleted_at IS NULL`, roleKey).Scan(&name) == nil {
		return name
	}
	return roleKey
}

// RoleKeyByName 은 역할 이름(예: 직원·구매 전담)의 키다. 없으면 빈 문자열.
func RoleKeyByName(ctx context.Context, q labelQ, name string) string {
	var key string
	if q.QueryRowContext(ctx, `SELECT key FROM roles WHERE name = ? AND deleted_at IS NULL`, strings.TrimSpace(name)).Scan(&key) == nil {
		return key
	}
	return ""
}

// EnsureCompatGrant 는 사용자에게 users.role(역할 키)과 같은 역할의 직접 부여(source=compat)가 있게 한다. 템플릿 키는 users 트리거가 만들고,
// 사용자 정의 역할 키(조직 가져오기 등)는 이 함수가 만든다. 이미 있으면 아무것도 하지 않는다.
func EnsureCompatGrant(ctx context.Context, tx Execer, userID int64, roleKey string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO role_grants (role_id, target_type, target_id, source, reason, created_at)
		SELECT r.id, 'user', ?, 'compat', 'users.role 호환', ? FROM roles r WHERE r.key = ? AND r.deleted_at IS NULL ON CONFLICT DO NOTHING`, userID, now.UTC().Format(time.RFC3339Nano), roleKey)
	return err
}

// RiskyRoleError 는 위험 권한이 든 역할을 호환 경로(사용자 수정·입사 발령·조직 가져오기)로 지정하려 한 거부다. contract.ErrValidation 이다.
// 위험 권한이 든 역할은 역할 관리 화면의 부여(2인 확인)로만 줄 수 있다.
type RiskyRoleError struct{ Perms []string }

func (e *RiskyRoleError) Error() string {
	return "위험 권한이 들어 있는 역할은 여기서 지정할 수 없습니다. 먼저 일반 역할로 처리한 뒤 역할 관리 화면에서 부여(2인 확인)하세요."
}
func (e *RiskyRoleError) Is(target error) bool { return target == contract.ErrValidation }

type riskQ interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// RiskyPerms 는 역할(키)에 든 위험 권한(contract.HighRiskPermissions)이다. 템플릿·사용자 정의 모두 DB 의 현재 구성으로 본다.
func RiskyPerms(ctx context.Context, q riskQ, roleKey string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT rp.permission FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.key = ? AND r.deleted_at IS NULL ORDER BY rp.permission`, roleKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		for _, r := range contract.HighRiskPermissions {
			if string(r) == p {
				out = append(out, p)
			}
		}
	}
	return out, rows.Err()
}

// CheckAssignable 은 이 역할을 호환 경로로 지정해도 되는지다. 위험 권한이 들어 있으면 *RiskyRoleError 다. 세 경로(사용자 수정·입사 발령·조직 가져오기)가 이 한 곳을 쓴다.
func CheckAssignable(ctx context.Context, q riskQ, roleKey string) error {
	risky, err := RiskyPerms(ctx, q, roleKey)
	if err != nil {
		return err
	}
	if len(risky) > 0 {
		return &RiskyRoleError{Perms: risky}
	}
	return nil
}

// RiskLabels 는 위험 권한의 한국어 이름이다(오류·감사 문구용).
var RiskLabels = map[string]string{
	"role:manage": "역할·부여 관리", "auth:manage": "로그인 설정 관리", "audit:verify": "감사 체인 검증", "user:manage": "사용자 관리",
	"mask:bypass": "마스킹 해제", "retention:dispose": "문서 폐기 승인", "llm:policy_manage": "LLM 정책 관리", "ledger:write": "전표 입력·승인 요청", "payment:write": "지급 처리",
}

// RiskLabel 은 위험 권한 하나의 한국어 이름이다(없으면 "위험 권한").
func RiskLabel(perm string) string {
	if l, ok := RiskLabels[perm]; ok {
		return l
	}
	return "위험 권한"
}

// RiskSnapshot 은 지금 실효 권한 캐시에서 사용자별 위험 권한 집합을 읽는다(위험 권한이 없는 사용자는 빠진다).
func RiskSnapshot(ctx context.Context, q riskQ) (map[int64]map[string]bool, error) {
	marks, args := make([]string, 0, len(contract.HighRiskPermissions)), make([]any, 0, len(contract.HighRiskPermissions))
	for _, r := range contract.HighRiskPermissions {
		marks, args = append(marks, "?"), append(args, string(r))
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT user_id, permission FROM user_effective_permissions WHERE permission IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]bool{}
	for rows.Next() {
		var uid int64
		var p string
		if err := rows.Scan(&uid, &p); err != nil {
			return nil, err
		}
		if out[uid] == nil {
			out[uid] = map[string]bool{}
		}
		out[uid][p] = true
	}
	return out, rows.Err()
}
