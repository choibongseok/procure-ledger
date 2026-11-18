package org

import (
	"context"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// DemoPerson 은 데모 데이터의 사람 한 명이다. 이메일은 실제로 존재할 수 없는 예약 도메인(.invalid)이라 외부 로그인으로도 만날 수 없고,
// 비밀번호·외부 연결은 만들지 않는다(표시용 계정, 명세 38 4.6).
type DemoPerson struct {
	Key   string // 코드에서 사람을 가리키는 이름(예: "purchase.lead")
	Name  string
	Email string
	Rank  string
	Unit  string // 소속 조직 코드
	Role  contract.Role
	Head  bool // 소속 조직의 조직장으로 보임한다
}

// DemoUnits 는 데모 조직이다(결정적). 회사 아래 본부·부서·팀이 구매 결재선과 재무 결재선에 필요한 만큼만 있다.
var DemoUnits = []seedUnit{
	{"DIV-MG", "경영지원본부", contract.OrgDivision, "CO", ""},
	{"DIV-PL", "구매·물류본부", contract.OrgDivision, "CO", ""},
	{"DIV-SL", "영업본부", contract.OrgDivision, "CO", ""},
	{"DEP-MG", "경영지원팀", contract.OrgDepartment, "DIV-MG", ""},
	{"DEP-PU", "구매팀", contract.OrgDepartment, "DIV-PL", ""},
	{"DEP-SL", "영업팀", contract.OrgDepartment, "DIV-SL", ""},
	{"T-FIN", "재무팀", contract.OrgTeam, "DEP-MG", ""},
	{"T-PU1", "구매1팀", contract.OrgTeam, "DEP-PU", ""},
	{"T-SL1", "영업1팀", contract.OrgTeam, "DEP-SL", ""},
}

// DemoPeople 은 데모 사람들이다(결정적). 모든 이메일은 @demo.invalid 이다.
var DemoPeople = []DemoPerson{
	{"ceo", "신대표", "ceo@demo.invalid", "대표이사", "CO", contract.RoleManager, true},
	{"mg.div", "오본부", "mg.div@demo.invalid", "전무", "DIV-MG", contract.RoleManager, true},
	{"pl.div", "최본부", "pl.div@demo.invalid", "상무", "DIV-PL", contract.RoleManager, true},
	{"sl.div", "강본부", "sl.div@demo.invalid", "상무", "DIV-SL", contract.RoleManager, true},
	{"mg.dep", "한부서", "mg.dep@demo.invalid", "부장", "DEP-MG", contract.RoleManager, true},
	{"purchase.dep", "정구매", "purchase.dep@demo.invalid", "부장", "DEP-PU", contract.RoleManager, true},
	{"sales.dep", "윤영업", "sales.dep@demo.invalid", "부장", "DEP-SL", contract.RoleManager, true},
	{"finance.lead", "임재무", "finance.lead@demo.invalid", "차장", "T-FIN", contract.RoleManager, true},
	{"finance.a", "서재무", "finance.a@demo.invalid", "과장", "T-FIN", contract.RoleFinance, false},
	{"finance.b", "류재무", "finance.b@demo.invalid", "대리", "T-FIN", contract.RoleFinance, false},
	{"purchase.lead", "이도윤", "purchase.lead@demo.invalid", "차장", "T-PU1", contract.RoleManager, true},
	{"purchase.staff1", "박서연", "purchase.staff1@demo.invalid", "대리", "T-PU1", contract.RoleStaff, false},
	{"purchase.staff2", "김지후", "purchase.staff2@demo.invalid", "사원", "T-PU1", contract.RoleStaff, false},
	{"sales.lead", "조영업", "sales.lead@demo.invalid", "차장", "T-SL1", contract.RoleManager, true},
	{"sales.staff", "황영업", "sales.staff@demo.invalid", "사원", "T-SL1", contract.RoleStaff, false},
	{"auditor", "송감사", "auditor@demo.invalid", "과장", "T-FIN", contract.RoleAuditor, false},
}

// DemoOrg 는 SeedDemoOrg 의 결과다: 조직 코드 → 번호, 사람 키 → 사용자 번호.
type DemoOrg struct {
	Units map[string]int64
	Users map[string]int64
}

// SeedDemoOrg 는 데모 조직·사람을 실제 발령 경로(Command → 이벤트)로 넣는다. 조직 이벤트가 하나도 없는 DB 에서만 되고(아니면 오류),
// 개발 시드(org.SeedDev, @pl.test)와는 이메일이 겹치지 않는다(조직 코드는 시스템 양식의 결재선이 가리키는 표준 코드를 쓰므로 같고, 두 시드는 한 DB 에 함께 들어가지 않는다). 날짜는 시계 기준 상대값이라 같은 입력이면 같은 구조가 나온다.
func (s *Service) SeedDemoOrg(ctx context.Context) (DemoOrg, error) {
	out := DemoOrg{Units: map[string]int64{}, Users: map[string]int64{}}
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_events`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("조직이 이미 있어 데모 데이터를 넣을 수 없습니다(데모는 설치 때만 고를 수 있습니다)")
		}
		today := s.Today()
		orgDay := AddDays(today, -420)
		hireDay := AddDays(today, -400)
		headDay := AddDays(today, -390)
		actor := systemActor
		reason := "데모 데이터"
		batch := func(cmds []Command) ([]Outcome, error) { return s.RegisterBatch(ctx, tx, actor, cmds, true) }
		rankID := map[string]int64{}
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM org_ranks`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			rankID[name] = id
		}
		if err := rows.Close(); err != nil {
			return err
		}
		o, err := batch([]Command{{Kind: KindUnitCreate, Code: "CO", Name: "데모 주식회사", UnitKind: contract.OrgCompany, Effective: orgDay, Reason: reason, Confirm: true}})
		if err != nil {
			return fmt.Errorf("데모 회사: %w", err)
		}
		out.Units["CO"] = o[0].UnitID
		for _, u := range DemoUnits {
			o, err := batch([]Command{{Kind: KindUnitCreate, Code: u.code, Name: u.name, UnitKind: u.kind, ParentID: out.Units[u.parent], Effective: orgDay, Reason: reason, Confirm: true}})
			if err != nil {
				return fmt.Errorf("데모 조직 %s: %w", u.name, err)
			}
			out.Units[u.code] = o[0].UnitID
		}
		var hires []Command
		for _, p := range DemoPeople {
			hires = append(hires, Command{Kind: KindHire, Name: p.Name, Email: p.Email, Role: p.Role, Rank: rankID[p.Rank], ToUnit: out.Units[p.Unit],
				Effective: hireDay, Reason: reason, Confirm: true, trustedRole: true})
		}
		hout, err := batch(hires)
		if err != nil {
			return fmt.Errorf("데모 사람: %w", err)
		}
		var heads []Command
		for i, p := range DemoPeople {
			out.Users[p.Key] = hout[i].UserID
			if p.Head {
				heads = append(heads, Command{Kind: KindHeadSet, UnitID: out.Units[p.Unit], UserID: hout[i].UserID, Effective: headDay, Reason: reason, Confirm: true})
			}
		}
		if _, err := batch(heads); err != nil {
			return fmt.Errorf("데모 조직장: %w", err)
		}
		return nil
	})
	s.invalidate()
	return out, err
}

// PlaceUser 는 조직에 아직 올라 있지 않은 기존 사용자(설치 때 만든 첫 관리자)를 조직 코드·직위 이름으로 배치한다.
// 입사 발령(KindHire, 기존 사용자 번호)을 발령자 actor 의 이름으로 등록하므로 발령 이력과 감사 기록이 남고, 위험 권한 획득 판정(applyRiskGain)을
// 그대로 거친다(actor 는 시스템이 아니라 사용자여야 한다: 시스템 발령자만 그 판정을 건너뛴다). 효력일은 오늘이다.
// 멱등이다: 이미 입사 발령이 있는 사람은 아무것도 하지 않고 false 를 돌려준다.
func (s *Service) PlaceUser(ctx context.Context, actor contract.Principal, userID int64, unitCode, rankName string) (bool, error) {
	if actor.UserID == 0 {
		return false, fmt.Errorf("배치는 사용자 이름으로만 등록합니다")
	}
	placed := false
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_events WHERE subject_type = 'person' AND subject_id = ? AND kind = ?`, userID, KindHire).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		var unitID, rankID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM org_units WHERE code = ?`, unitCode).Scan(&unitID); err != nil {
			return fmt.Errorf("조직 %s 를 찾을 수 없습니다: %w", unitCode, err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM org_ranks WHERE name = ?`, rankName).Scan(&rankID); err != nil {
			return fmt.Errorf("직위 %s 를 찾을 수 없습니다: %w", rankName, err)
		}
		_, err := s.RegisterBatch(ctx, tx, actor, []Command{{Kind: KindHire, UserID: userID, Rank: rankID, ToUnit: unitID, Reason: "데모 데이터 첫 관리자 배치", Confirm: true}}, true)
		placed = err == nil
		return err
	})
	s.invalidate()
	return placed, err
}
