package org

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// Querier 는 *sql.DB 와 contract.Tx 가 모두 만족하는 읽기 인터페이스다.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Append 는 이벤트 로그에 한 줄을 추가하고 등록 번호를 돌려준다. 로그는 추가 전용이다(DB 트리거).
// 구간 테이블은 건드리지 않는다: 호출자가 같은 트랜잭션에서 Rebuild 를 부른다.
func Append(ctx context.Context, tx contract.Tx, e Event) (int64, error) {
	payload, err := json.Marshal(e.P)
	if err != nil {
		return 0, err
	}
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO org_events
		(subject_type, subject_id, kind, effective_date, payload, reason, cancels_id, corrects_id, created_by)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		e.Subject, e.SubjectID, e.Kind, string(e.Effective), string(payload), e.Reason, nullID(e.Cancels), nullID(e.Corrects), e.CreatedBy)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// LoadEvents 는 이벤트 로그 전체를 등록 번호 순으로 읽는다.
func LoadEvents(ctx context.Context, q Querier) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, subject_type, subject_id, kind, effective_date, payload, reason,
		COALESCE(cancels_id,0), COALESCE(corrects_id,0), created_by FROM org_events ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var day, payload string
		if err := rows.Scan(&e.ID, &e.Subject, &e.SubjectID, &e.Kind, &day, &payload, &e.Reason, &e.Cancels, &e.Corrects, &e.CreatedBy); err != nil {
			return nil, err
		}
		e.Effective = contract.Day(day)
		if err := json.Unmarshal([]byte(payload), &e.P); err != nil {
			return nil, fmt.Errorf("이벤트 %d 내용: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NewUnitID 는 조직 식별자(번호·코드)를 만든다. explicitID 가 0 이 아니면 그 번호를 쓴다(기존 부서 번호 유지용).
func NewUnitID(ctx context.Context, tx contract.Tx, code string, explicitID int64) (int64, error) {
	if explicitID != 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO org_units (id, code) VALUES (?, ?)`, explicitID, code); err != nil {
			return explicitID, err
		}
		return explicitID, dbutil.SyncSequence(ctx, tx, "org_units")
	}
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO org_units (code) VALUES (?)`, code)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// withGuard 는 쓰기 보호 행을 넣고 fn 을 돈 뒤 지운다(fn 이 실패하면 호출자의 롤백이 함께 지운다).
func withGuard(ctx context.Context, tx contract.Tx, fn func() error) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO org_write_guard (id) VALUES (1)`); err != nil {
		return fmt.Errorf("쓰기 보호 행: %w", err)
	}
	if err := fn(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM org_write_guard`)
	return err
}

// Rebuild 는 이벤트 로그 전체를 Fold 해서 구간 테이블을 다시 쓴다. 로그가 규칙을 어기면 *FoldError 를 돌려주고
// 아무것도 바꾸지 않는다. 호출자는 같은 트랜잭션에서 이벤트 추가와 Rebuild 를 하고, 오류면 롤백한다.
func Rebuild(ctx context.Context, tx contract.Tx) (*Result, error) {
	events, err := LoadEvents(ctx, tx)
	if err != nil {
		return nil, err
	}
	res, err := Fold(events)
	if err != nil {
		return nil, err
	}
	if err := WriteSpans(ctx, tx, res); err != nil {
		return nil, err
	}
	return res, nil
}

// WriteSpans 는 계산 결과로 구간 테이블을 바꿔 쓴다.
func WriteSpans(ctx context.Context, tx contract.Tx, r *Result) error {
	return withGuard(ctx, tx, func() error {
		for _, t := range []string{"org_unit_spans", "org_person_spans", "org_concurrent_spans", "org_acting_spans"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
				return err
			}
		}
		entityOf := unitEntities(ctx, tx, r.Units)
		for _, s := range r.Units {
			if _, err := tx.ExecContext(ctx, `INSERT INTO org_unit_spans
				(unit_id, valid_from, valid_to, code, name, kind, parent_id, sort_order, closed, head_user_id, head_acting, entity_id)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, s.UnitID, string(s.From), nullDay(s.To), s.Code, s.Name, string(s.Kind), s.ParentID, s.Sort,
				boolInt(s.Closed), s.HeadUser, boolInt(s.HeadActing), entityOf(s.UnitID)); err != nil {
				return err
			}
		}
		for _, s := range r.People {
			if _, err := tx.ExecContext(ctx, `INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id, absence_kind)
				VALUES (?,?,?,?,?,?,?)`, s.UserID, string(s.From), nullDay(s.To), boolInt(s.Left), s.Rank, s.Primary, s.Absence); err != nil {
				return err
			}
		}
		for _, s := range r.Concurrent {
			if _, err := tx.ExecContext(ctx, `INSERT INTO org_concurrent_spans (user_id, unit_id, valid_from, valid_to) VALUES (?,?,?,?)`,
				s.UserID, s.UnitID, string(s.From), nullDay(s.To)); err != nil {
				return err
			}
		}
		for _, s := range r.Acting {
			if _, err := tx.ExecContext(ctx, `INSERT INTO org_acting_spans (unit_id, user_id, valid_from, valid_to) VALUES (?,?,?,?)`,
				s.UnitID, s.UserID, string(s.From), nullDay(s.To)); err != nil {
				return err
			}
		}
		return nil
	})
}

// unitEntities 는 조직의 법인을 루트에서 물려받는 파생 값으로 계산한다(B44 7절). 조직의 부모는 가장 늦은 구간의 값을 따르고,
// 루트(부모 없음)가 entities.root_unit_id 에 연결돼 있으면 그 법인, 아니면 기본 법인이다. 법인 표가 없는 스키마(이관 전)에서는 항상 기본 법인(1)이다.
func unitEntities(ctx context.Context, tx contract.Tx, units []UnitSpan) func(int64) int64 {
	byRoot := map[int64]int64{}
	def := contract.DefaultEntityID
	if rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(root_unit_id, 0), is_default FROM entities`); err == nil {
		for rows.Next() {
			var id, root, isDef int64
			if rows.Scan(&id, &root, &isDef) == nil {
				if root > 0 {
					byRoot[root] = id
				}
				if isDef == 1 {
					def = id
				}
			}
		}
		rows.Close()
	}
	parent := map[int64]int64{}
	latest := map[int64]string{}
	for _, u := range units {
		if f := string(u.From); f >= latest[u.UnitID] {
			latest[u.UnitID], parent[u.UnitID] = f, u.ParentID
		}
	}
	cache := map[int64]int64{}
	return func(unit int64) int64 {
		if e, ok := cache[unit]; ok {
			return e
		}
		cur := unit
		for i := 0; i < 64; i++ {
			p, ok := parent[cur]
			if !ok || p == 0 {
				break
			}
			cur = p
		}
		e, ok := byRoot[cur]
		if !ok {
			e = def
		}
		cache[unit] = e
		return e
	}
}

func nullDay(d contract.Day) any {
	if d == "" {
		return nil
	}
	return string(d)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LoadSpans 는 저장된 구간 테이블을 Fold 결과와 같은 모양으로 읽는다(정합성 검사·조회 구현이 쓴다).
func LoadSpans(ctx context.Context, q Querier) (*Result, error) {
	var r Result
	rows, err := q.QueryContext(ctx, `SELECT unit_id, valid_from, COALESCE(valid_to,''), code, name, kind, parent_id, sort_order, closed, head_user_id, head_acting
		FROM org_unit_spans ORDER BY unit_id, valid_from`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s UnitSpan
		var from, to, kind string
		var closed, acting int
		if err := rows.Scan(&s.UnitID, &from, &to, &s.Code, &s.Name, &kind, &s.ParentID, &s.Sort, &closed, &s.HeadUser, &acting); err != nil {
			rows.Close()
			return nil, err
		}
		s.From, s.To, s.Kind, s.Closed, s.HeadActing = contract.Day(from), contract.Day(to), contract.OrgKind(kind), closed == 1, acting == 1
		r.Units = append(r.Units, s)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT user_id, valid_from, COALESCE(valid_to,''), left_flag, rank_id, primary_unit_id, absence_kind FROM org_person_spans ORDER BY user_id, valid_from`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s PersonSpan
		var from, to string
		var left int
		if err := rows.Scan(&s.UserID, &from, &to, &left, &s.Rank, &s.Primary, &s.Absence); err != nil {
			rows.Close()
			return nil, err
		}
		s.From, s.To, s.Left = contract.Day(from), contract.Day(to), left == 1
		r.People = append(r.People, s)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT user_id, unit_id, valid_from, COALESCE(valid_to,'') FROM org_concurrent_spans ORDER BY user_id, unit_id, valid_from`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s ConcurrentSpan
		var from, to string
		if err := rows.Scan(&s.UserID, &s.UnitID, &from, &to); err != nil {
			rows.Close()
			return nil, err
		}
		s.From, s.To = contract.Day(from), contract.Day(to)
		r.Concurrent = append(r.Concurrent, s)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT unit_id, user_id, valid_from, COALESCE(valid_to,'') FROM org_acting_spans ORDER BY unit_id, valid_from`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s ActingSpan
		var from, to string
		if err := rows.Scan(&s.UnitID, &s.UserID, &from, &to); err != nil {
			rows.Close()
			return nil, err
		}
		s.From, s.To = contract.Day(from), contract.Day(to)
		r.Acting = append(r.Acting, s)
	}
	rows.Close()
	return &r, nil
}

// Consistent 는 저장된 구간 테이블이 이벤트 로그를 처음부터 다시 계산한 결과와 같은지 본다(명세 AC-61).
// 다르면 어디가 다른지 설명하는 오류를 돌려준다.
func Consistent(ctx context.Context, q Querier) error {
	events, err := LoadEvents(ctx, q)
	if err != nil {
		return err
	}
	want, err := Fold(events)
	if err != nil {
		return fmt.Errorf("발령 이력이 규칙을 어김: %w", err)
	}
	got, err := LoadSpans(ctx, q)
	if err != nil {
		return err
	}
	return compareResults(want, got)
}

// ConsistencyReport 는 저장된 구간 테이블이 이벤트 로그를 다시 계산한 결과와 같은지와, 어긋난다면 어긋난 구간 수다.
// 로그 자체가 규칙을 어기면 Broken 에 이유가 있다.
type ConsistencyReport struct {
	OK         bool
	Mismatches int
	Broken     string
}

func diffCount[T comparable](want, got []T) int {
	seen := map[T]int{}
	for _, w := range want {
		seen[w]++
	}
	n := 0
	for _, g := range got {
		if seen[g] > 0 {
			seen[g]--
		} else {
			n++
		}
	}
	for _, c := range seen {
		n += c
	}
	return n
}

// CheckConsistency 는 Consistent 와 같은 비교를 하되 오류 대신 어긋난 구간 수를 센다(조직 관리 화면의 점검용, 읽기만 한다).
func CheckConsistency(ctx context.Context, q Querier) (ConsistencyReport, error) {
	events, err := LoadEvents(ctx, q)
	if err != nil {
		return ConsistencyReport{}, err
	}
	want, err := Fold(events)
	if err != nil {
		return ConsistencyReport{Broken: err.Error()}, nil
	}
	got, err := LoadSpans(ctx, q)
	if err != nil {
		return ConsistencyReport{}, err
	}
	clear := func(r *Result) {
		for i := range r.Units {
			r.Units[i].dead = false
		}
		for i := range r.People {
			r.People[i].dead = false
		}
		for i := range r.Concurrent {
			r.Concurrent[i].dead = false
		}
	}
	clear(want)
	clear(got)
	n := diffCount(want.Units, got.Units) + diffCount(want.People, got.People) + diffCount(want.Concurrent, got.Concurrent) + diffCount(want.Acting, got.Acting)
	return ConsistencyReport{OK: n == 0, Mismatches: n}, nil
}

func compareResults(want, got *Result) error {
	if len(want.Units) != len(got.Units) {
		return fmt.Errorf("조직 구간 수가 다릅니다(계산 %d, 저장 %d)", len(want.Units), len(got.Units))
	}
	for i := range want.Units {
		w, g := want.Units[i], got.Units[i]
		w.dead, g.dead = false, false
		if w != g {
			return fmt.Errorf("조직 구간이 다릅니다(계산 %+v, 저장 %+v)", w, g)
		}
	}
	if len(want.People) != len(got.People) {
		return fmt.Errorf("사람 구간 수가 다릅니다(계산 %d, 저장 %d)", len(want.People), len(got.People))
	}
	for i := range want.People {
		w, g := want.People[i], got.People[i]
		w.dead, g.dead = false, false
		if w != g {
			return fmt.Errorf("사람 구간이 다릅니다(계산 %+v, 저장 %+v)", w, g)
		}
	}
	if len(want.Concurrent) != len(got.Concurrent) {
		return fmt.Errorf("겸직 구간 수가 다릅니다(계산 %d, 저장 %d)", len(want.Concurrent), len(got.Concurrent))
	}
	for i := range want.Concurrent {
		w, g := want.Concurrent[i], got.Concurrent[i]
		w.dead, g.dead = false, false
		if w != g {
			return fmt.Errorf("겸직 구간이 다릅니다(계산 %+v, 저장 %+v)", w, g)
		}
	}
	if len(want.Acting) != len(got.Acting) {
		return fmt.Errorf("직무대행 구간 수가 다릅니다(계산 %d, 저장 %d)", len(want.Acting), len(got.Acting))
	}
	for i := range want.Acting {
		if want.Acting[i] != got.Acting[i] {
			return fmt.Errorf("직무대행 구간이 다릅니다(계산 %+v, 저장 %+v)", want.Acting[i], got.Acting[i])
		}
	}
	return nil
}

// Consistency 는 지금 저장소의 이벤트 로그와 구간 테이블이 일치하는지 점검한다.
func (s *Service) Consistency(ctx context.Context) (ConsistencyReport, error) {
	return CheckConsistency(ctx, s.db)
}
