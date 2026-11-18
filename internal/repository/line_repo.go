package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/holders"
)

// LineUserRec 는 결재선 만들기·재지정이 읽는 사용자 요약이다. Perms 는 실효 권한(user_effective_permissions, B33)이다.
// 결재 자격·재무 풀 판정은 역할 이름이 아니라 이 권한으로 한다. Role 은 표시용이다.
type LineUserRec struct {
	ID     int64
	Name   string
	Role   contract.Role
	Active bool
	Perms  contract.PermSet
}

// Can 은 사용자가 실효 권한을 가졌는지다.
func (u LineUserRec) Can(p contract.Permission) bool { return u.Perms.Has(p) }

// SQLLines 는 결재선 만들기가 읽는 사용자 조회다(B21).
type SQLLines struct{}

// effectivePerms 는 사용자들의 실효 권한을 한 번에 읽는다(없는 사용자는 빈 집합). 열린 rows 가 없을 때 부른다(연결 1개).
func effectivePerms(ctx context.Context, q contract.Tx, ids []int64) (map[int64]contract.PermSet, error) {
	out := map[int64]contract.PermSet{}
	for _, id := range ids {
		out[id] = contract.NewPermSet()
	}
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT user_id, permission FROM user_effective_permissions WHERE user_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var perm string
		if err := rows.Scan(&id, &perm); err != nil {
			return nil, err
		}
		out[id][contract.Permission(perm)] = struct{}{}
	}
	return out, rows.Err()
}

// User 는 사용자 한 명이다. 없으면 contract.ErrNotFound.
func (SQLLines) User(ctx context.Context, q contract.Tx, id int64) (LineUserRec, error) {
	var u LineUserRec
	var role string
	var active int
	err := q.QueryRowContext(ctx, `SELECT id, name, role, active FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Name, &role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return u, contract.ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.Role, u.Active = contract.Role(role), active == 1
	perms, err := effectivePerms(ctx, q, []int64{id})
	u.Perms = perms[id]
	return u, err
}

// ActiveByPerm 은 사용 중이면서 그 실효 권한을 가진 사용자 전원이다(id 순).
func (SQLLines) ActiveByPerm(ctx context.Context, q contract.Tx, perm contract.Permission) ([]LineUserRec, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, role, active FROM users u WHERE active = 1
		AND EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = ?) ORDER BY id`, string(perm))
	if err != nil {
		return nil, err
	}
	var out []LineUserRec
	for rows.Next() {
		var u LineUserRec
		var r string
		var active int
		if err := rows.Scan(&u.ID, &u.Name, &r, &active); err != nil {
			rows.Close()
			return nil, err
		}
		u.Role, u.Active = contract.Role(r), active == 1
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// 다법인(B44): 현재 법인에서 그 권한이 효력인 사람만 후보다(다른 법인 전용 부여·소속은 빠진다).
	if out, err = filterByEntityHolders(ctx, q, perm, out, func(u LineUserRec) int64 { return u.ID }); err != nil {
		return nil, err
	}
	ids := make([]int64, len(out))
	for i, u := range out {
		ids[i] = u.ID
	}
	perms, err := effectivePerms(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Perms = perms[out[i].ID]
	}
	return out, nil
}

// OrgVersion 은 결재선 계산이 기대는 상태의 지문이다: 조직 이벤트의 마지막 번호와 사용자의 (번호·역할·사용 여부) 목록.
// 상신 트랜잭션이 계산 전에 읽은 값과 비교해 다르면 결재선을 다시 만든다.
func (SQLLines) OrgVersion(ctx context.Context, q contract.Tx) (string, error) {
	var ev, users string
	if err := q.QueryRowContext(ctx, `SELECT CAST(COALESCE(MAX(id),0) AS TEXT) FROM org_events`).Scan(&ev); err != nil {
		return "", err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(string_agg(id || ':' || role || ':' || active, ','),'') FROM (SELECT id, role, active FROM users ORDER BY id)`).Scan(&users); err != nil {
		return "", err
	}
	// 결재 자격·재무 풀은 실효 권한이 정하므로 그 변화도 지문에 넣는다(역할 부여만 바뀌어도 결재선을 다시 만든다).
	var perms string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(string_agg(user_id || ':' || permission, ','),'') FROM (SELECT DISTINCT user_id, permission FROM user_effective_permissions
		WHERE permission IN ('approval:decide','approval:agree','approval:finance_stage') ORDER BY user_id, permission)`).Scan(&perms); err != nil {
		return "", err
	}
	return ev + "|" + users + "|" + perms, nil
}

// filterByEntityHolders 는 목록을 현재 법인에서 권한 perm 이 효력인 사용자로 거른다. 현재 법인이 없는 context(서버 내부 작업·시험)는 거르지 않는다.
func filterByEntityHolders[T any](ctx context.Context, q contract.Tx, perm contract.Permission, in []T, idOf func(T) int64) ([]T, error) {
	a, ok := contract.ActiveEntityFrom(ctx)
	if !ok || a.All || a.ID <= 0 {
		return in, nil
	}
	ids, err := holders.Of(ctx, q, perm, a.ID, contract.DayOf(time.Now()))
	if err != nil {
		return nil, err
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	out := in[:0:0]
	for _, x := range in {
		if set[idOf(x)] {
			out = append(out, x)
		}
	}
	return out, nil
}
