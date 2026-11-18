package access

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// Targets 는 부여 대상 선택에 쓰는 목록이다(역할 관리 화면). 사용자·조직·직위 화면 권한이 없어도 역할 관리자가 대상을 고를 수 있게 한다.
type Targets struct {
	Users []TargetUser `json:"users"`
	Units []TargetUnit `json:"units"`
	Ranks []TargetRank `json:"ranks"`
	// Entities 는 부여 범위로 고를 수 있는 활성 법인이다(B44). 법인이 하나뿐이면 화면이 범위 선택을 그리지 않는다.
	Entities []TargetEntity `json:"entities"`
}

// TargetEntity 는 선택 가능한 활성 법인이다.
type TargetEntity struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// TargetUser 는 선택 가능한 사용자(활성)다.
type TargetUser struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// TargetUnit 은 오늘 있는 조직이다.
type TargetUnit struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// TargetRank 는 사용 중인 직위다.
type TargetRank struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Targets 는 부여 대상 후보 목록이다(role:manage).
func (s *Service) Targets(ctx context.Context, p contract.Principal) (Targets, error) {
	if err := s.authorize(ctx, p); err != nil {
		return Targets{}, err
	}
	out := Targets{Users: []TargetUser{}, Units: []TargetUnit{}, Ranks: []TargetRank{}, Entities: []TargetEntity{}}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, email FROM users WHERE active = 1 ORDER BY name, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var u TargetUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Email); err != nil {
			rows.Close()
			return out, err
		}
		out.Users = append(out.Users, u)
	}
	rows.Close()
	d := string(s.today())
	urows, err := s.db.QueryContext(ctx, `SELECT unit_id, name, kind FROM org_unit_spans WHERE closed = 0 AND valid_from <= ?1 AND (valid_to IS NULL OR valid_to > ?1) ORDER BY sort_order, name`, d)
	if err != nil {
		return out, err
	}
	for urows.Next() {
		var u TargetUnit
		if err := urows.Scan(&u.ID, &u.Name, &u.Kind); err != nil {
			urows.Close()
			return out, err
		}
		out.Units = append(out.Units, u)
	}
	urows.Close()
	erows, err := s.db.QueryContext(ctx, `SELECT id, code, name FROM entities WHERE active = 1 ORDER BY is_default DESC, id`)
	if err != nil {
		return out, err
	}
	for erows.Next() {
		var e TargetEntity
		if err := erows.Scan(&e.ID, &e.Code, &e.Name); err != nil {
			erows.Close()
			return out, err
		}
		out.Entities = append(out.Entities, e)
	}
	erows.Close()
	rrows, err := s.db.QueryContext(ctx, `SELECT id, name FROM org_ranks WHERE active = 1 ORDER BY sort_order`)
	if err != nil {
		return out, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var r TargetRank
		if err := rrows.Scan(&r.ID, &r.Name); err != nil {
			return out, err
		}
		out.Ranks = append(out.Ranks, r)
	}
	return out, rrows.Err()
}
