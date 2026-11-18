package org

import (
	"context"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

const settingDevSeed = "org_dev_seed"

// SeedDev 는 dev IdP 가 켜진 개발·시연 환경에 현실적인 조직(회사 1, 본부 4, 부서 5, 팀 12, 재직 약 60명)을 넣는다(명세 13절).
// 실제 발령 경로(Command → 이벤트)로 넣어 과거 이력이 쌓이고, 조직 이벤트가 하나도 없는 DB 에서만 한 번 실행된다(멱등).
// 기존 dev 계정 9개(admin@pl.test 등)와 부서 3개(구매팀·경영지원팀·영업팀)는 번호를 이어 쓰고 명세 13절 표의 자리에 놓인다.
// 시계 기준 날짜에서 상대적으로 사례(공석·직무대행, 예약 전보, 퇴사·재입사, 취소·정정, 폐지 조직)를 만든다. 넣었으면 true.
func (s *Service) SeedDev(ctx context.Context) (bool, error) {
	var did bool
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, 'running') ON CONFLICT DO NOTHING`, settingDevSeed); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_events`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if err := s.seedDevTx(ctx, tx); err != nil {
			return err
		}
		did = true
		_, err := tx.ExecContext(ctx, `UPDATE app_settings SET value = 'done' WHERE key = ?`, settingDevSeed)
		return err
	})
	s.invalidate()
	return did, err
}

type seedUnit struct {
	code, name string
	kind       contract.OrgKind
	parent     string // 부모 코드
	legacyName string // 기존 dev 부서 이름(번호를 이어 쓴다)
}

type seedPerson struct {
	email, name string
	rank        string
	unit        string // 소속 조직 코드
	role        contract.Role
	hire        contract.Day
}

var devUnits = []seedUnit{
	{"DIV-MG", "경영지원본부", contract.OrgDivision, "CO", ""},
	{"DIV-PL", "구매·물류본부", contract.OrgDivision, "CO", ""},
	{"DIV-SL", "영업본부", contract.OrgDivision, "CO", ""},
	{"DIV-DV", "개발본부", contract.OrgDivision, "CO", ""},
	{"DEP-MG", "경영지원팀", contract.OrgDepartment, "DIV-MG", "경영지원팀"},
	{"DEP-PU", "구매팀", contract.OrgDepartment, "DIV-PL", "구매팀"},
	{"DEP-LG", "물류팀", contract.OrgDepartment, "DIV-PL", ""},
	{"DEP-SL", "영업팀", contract.OrgDepartment, "DIV-SL", "영업팀"},
	{"DEP-DV", "개발팀", contract.OrgDepartment, "DIV-DV", ""},
	{"T-HR", "인사팀", contract.OrgTeam, "DEP-MG", ""},
	{"T-FIN", "재무팀", contract.OrgTeam, "DEP-MG", ""},
	{"T-AUD", "감사팀", contract.OrgTeam, "DEP-MG", ""},
	{"T-PU1", "구매1팀", contract.OrgTeam, "DEP-PU", ""},
	{"T-PU2", "구매2팀", contract.OrgTeam, "DEP-PU", ""},
	{"T-PU3", "구매3팀", contract.OrgTeam, "DEP-PU", ""}, // 과거에 신설했다가 폐지하는 조직
	{"T-IN", "입고팀", contract.OrgTeam, "DEP-LG", ""},
	{"T-OUT", "출하팀", contract.OrgTeam, "DEP-LG", ""},
	{"T-SL1", "영업1팀", contract.OrgTeam, "DEP-SL", ""},
	{"T-SL2", "영업2팀", contract.OrgTeam, "DEP-SL", ""},
	{"T-SLS", "영업지원팀", contract.OrgTeam, "DEP-SL", ""},
	{"T-PLT", "플랫폼개발팀", contract.OrgTeam, "DEP-DV", ""},
	{"T-APP", "응용개발팀", contract.OrgTeam, "DEP-DV", ""},
}

var surnames = []string{"김", "이", "박", "최", "정", "강", "조", "윤", "장", "임", "한", "오", "서", "신", "권", "황", "안", "송", "류", "홍"}
var givens = []string{"민준", "서연", "지후", "하윤", "도윤", "서준", "예은", "시우", "지민", "수빈", "현우", "유진", "준서", "채원", "건우", "다은", "지원", "소율", "태양", "나은", "승현", "보람", "영호", "미래", "재현"}

// devPeople 은 새로 만드는 사람들이다(기존 dev 계정 9개는 따로 자리를 정한다). 이메일은 p001@pl.test 형식이다.
func devPeople() []seedPerson {
	var out []seedPerson
	i := 0
	add := func(name, rank, unit string, role contract.Role, year int) {
		i++
		month := 1 + (i*5)%12
		day := 1 + (i*7)%27
		out = append(out, seedPerson{email: fmt.Sprintf("p%03d@pl.test", i), name: name, rank: rank, unit: unit, role: role,
			hire: contract.Day(fmt.Sprintf("%04d-%02d-%02d", year, month, day))})
	}
	// 임원·부서장·팀장
	add("신대표", "대표이사", "CO", contract.RoleManager, 2016)
	add("오본부", "전무", "DIV-MG", contract.RoleManager, 2016)
	add("최본부", "상무", "DIV-PL", contract.RoleManager, 2016)
	add("강본부", "상무", "DIV-SL", contract.RoleManager, 2017)
	add("서본부", "이사", "DIV-DV", contract.RoleManager, 2017)
	add("한부서", "부장", "DEP-MG", contract.RoleManager, 2017)
	add("정물류", "부장", "DEP-LG", contract.RoleManager, 2018)
	add("윤영업", "부장", "DEP-SL", contract.RoleManager, 2018)
	add("임개발", "부장", "DEP-DV", contract.RoleManager, 2018)
	for k, t := range []string{"T-HR", "T-FIN", "T-AUD", "T-IN", "T-SL1", "T-SLS", "T-PLT", "T-APP"} {
		role := contract.RoleManager
		if t == "T-PLT" {
			// 결재 권한이 없는 조직장 사례(점검 탭에 나온다). 기존 구매 결재 흐름과 무관한 플랫폼개발팀에 하나만 둔다.
			role = contract.RoleStaff
		}
		add(surnames[(k*3+1)%20]+givens[(k*7+2)%25], "차장", t, role, 2019)
	}
	// 팀원
	members := map[string]int{"T-HR": 4, "T-FIN": 3, "T-AUD": 1, "T-PU1": 3, "T-PU2": 2, "T-IN": 4, "T-OUT": 4, "T-SL1": 4, "T-SL2": 3, "T-SLS": 2, "T-PLT": 3, "T-APP": 2}
	order := []string{"T-HR", "T-FIN", "T-AUD", "T-PU1", "T-PU2", "T-IN", "T-OUT", "T-SL1", "T-SL2", "T-SLS", "T-PLT", "T-APP"}
	ranks := []string{"사원", "대리", "과장", "사원", "대리", "사원", "과장", "대리"}
	for _, t := range order {
		for m := 0; m < members[t]; m++ {
			n := len(out)
			name := surnames[(n*3+5)%20] + givens[(n*11+3)%25]
			add(name, ranks[(n+m)%len(ranks)], t, contract.RoleStaff, 2019+(n%6))
		}
	}
	// 영업2팀 공석 중 직무대행을 맡는 결재자(영업2팀 직원의 상신이 막히지 않게 결재 권한이 있는 역할). 목록 맨 끝이라 앞 사람들의 번호는 그대로다.
	add("대행결재자", "과장", "T-SL1", contract.RoleManager, 2022)
	return out
}

func (s *Service) seedDevTx(ctx context.Context, tx contract.Tx) error {
	today := s.Today()
	rel := func(days int) contract.Day { return AddDays(today, days) }
	actor := systemActor
	batch := func(cmds []Command) ([]Outcome, error) { return s.RegisterBatch(ctx, tx, actor, cmds, true) }

	// 직위 번호
	rankID := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM org_ranks`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		rankID[n] = id
	}
	rows.Close()

	// 기존 dev 부서 번호
	legacyDept := map[string]int64{}
	var maxLegacy int64
	drows, err := tx.QueryContext(ctx, `SELECT id, name FROM departments`)
	if err != nil {
		return err
	}
	for drows.Next() {
		var id int64
		var n string
		if err := drows.Scan(&id, &n); err != nil {
			drows.Close()
			return err
		}
		legacyDept[n] = id
		if id > maxLegacy {
			maxLegacy = id
		}
	}
	drows.Close()

	// 1단계: 조직. 회사 번호는 기존 부서 번호와 겹치지 않게 그 다음 번호를 쓴다.
	cmds := []Command{{Kind: KindUnitCreate, Code: "CO", Name: "PL 주식회사", UnitKind: contract.OrgCompany, Effective: "2016-01-04", Reason: "개발용 시드", Confirm: true, FixedUnitID: maxLegacy + 1}}
	for _, u := range devUnits {
		cmds = append(cmds, Command{Kind: KindUnitCreate, Code: u.code, Name: u.name, UnitKind: u.kind, Effective: "2016-01-04", Reason: "개발용 시드", Confirm: true,
			FixedUnitID: legacyDept[u.legacyName]}) // 상위 조직 번호는 아래에서 채운다
	}
	ids := map[string]int64{}
	// 상위 조직 번호는 앞선 결과에서 알 수 있으므로 한 건씩 일괄 호출로 나눠 넣는다(같은 트랜잭션).
	out, err := batch(cmds[:1])
	if err != nil {
		return err
	}
	ids["CO"] = out[0].UnitID
	for i, u := range devUnits {
		c := cmds[i+1]
		c.ParentID = ids[u.parent]
		o, err := batch([]Command{c})
		if err != nil {
			return fmt.Errorf("조직 %s: %w", u.name, err)
		}
		ids[u.code] = o[0].UnitID
	}

	// 2단계: 사람. 기존 dev 계정 9개는 명세 13절 표의 자리에, 나머지는 새로 만든다.
	userByEmail := map[string]int64{}
	urows, err := tx.QueryContext(ctx, `SELECT id, email FROM users`)
	if err != nil {
		return err
	}
	for urows.Next() {
		var id int64
		var e string
		if err := urows.Scan(&id, &e); err != nil {
			urows.Close()
			return err
		}
		userByEmail[strings.ToLower(e)] = id
	}
	urows.Close()
	type legacy struct {
		email, name, rank, unit string
		role                    contract.Role
		hire                    contract.Day
	}
	legacies := []legacy{
		{"admin@pl.test", "관리자", "부장", "DEP-MG", contract.RoleAdmin, "2016-03-02"},
		{"finance@pl.test", "재무 담당", "과장", "T-FIN", contract.RoleFinance, "2019-03-04"},
		{"finance2@pl.test", "재무 담당2", "대리", "T-FIN", contract.RoleFinance, "2020-03-02"},
		{"lead@pl.test", "구매팀장", "차장", "T-PU1", contract.RoleManager, "2018-04-02"},
		{"lead2@pl.test", "구매부팀장", "차장", "T-PU2", contract.RoleManager, "2018-07-02"},
		{"head@pl.test", "구매부서장", "부장", "DEP-PU", contract.RoleManager, "2016-05-02"},
		{"staff@pl.test", "구매직원", "대리", "T-PU1", contract.RoleStaff, "2021-02-01"},
		{"staff2@pl.test", "영업직원", "사원", "T-SL1", contract.RoleStaff, "2023-03-02"},
		{"auditor@pl.test", "감사 담당", "과장", "T-AUD", contract.RoleAuditor, "2019-09-02"},
	}
	var hires []Command
	for _, l := range legacies {
		c := Command{Kind: KindHire, Rank: rankID[l.rank], ToUnit: ids[l.unit], Effective: l.hire, Reason: "개발용 시드", Confirm: true, trustedRole: true}
		if id, ok := userByEmail[l.email]; ok {
			c.UserID = id
		} else {
			c.Name, c.Email, c.Role = l.name, l.email, l.role
		}
		hires = append(hires, c)
	}
	people := devPeople()
	for i, p := range people {
		hires = append(hires, Command{Kind: KindHire, Name: p.name, Email: p.email, Role: p.role, Rank: rankID[p.rank], ToUnit: ids[p.unit], Effective: p.hire,
			Reason: "개발용 시드", Confirm: true, Phone: fmt.Sprintf("010-5555-%04d", 100+i), Extension: fmt.Sprintf("%d", 3000+i), trustedRole: true})
	}
	hout, err := batch(hires)
	if err != nil {
		return fmt.Errorf("개발용 사람: %w", err)
	}
	uid := map[string]int64{} // 이메일 → 사용자 번호
	for i, l := range legacies {
		uid[l.email] = hout[i].UserID
	}
	for i, p := range people {
		uid[p.email] = hout[len(legacies)+i].UserID
	}
	pid := func(n int) int64 { return uid[fmt.Sprintf("p%03d@pl.test", n)] } // p001 = 신대표 ...

	// 3단계: 조직장·겸직·직무대행·이력·사례
	var cmds3 []Command
	seed := func(c Command) {
		c.Reason, c.Confirm = "개발용 시드", true
		if c.Kind == KindLeave {
			c.Reason = "퇴사 처리"
		}
		cmds3 = append(cmds3, c)
	}
	hireOf := func(user int64) contract.Day {
		for i, p := range people {
			if user == uid[p.email] {
				_ = i
				return p.hire
			}
		}
		for _, l := range legacies {
			if user == uid[l.email] {
				return l.hire
			}
		}
		return "2016-01-04"
	}
	later := func(a, b contract.Day) contract.Day {
		if a > b {
			return a
		}
		return b
	}
	headAt := func(unit string, user int64, day contract.Day) {
		seed(Command{Kind: KindHeadSet, UnitID: ids[unit], UserID: user, Effective: later(day, AddDays(hireOf(user), 1))})
	}
	headAt("CO", pid(1), "2016-01-04")
	for i, d := range []string{"DIV-MG", "DIV-PL", "DIV-SL", "DIV-DV"} {
		headAt(d, pid(2+i), "2017-01-02")
	}
	for i, d := range []string{"DEP-MG", "DEP-LG", "DEP-SL", "DEP-DV"} {
		headAt(d, pid(6+i), "2018-06-01")
	}
	headAt("DEP-PU", uid["head@pl.test"], "2018-06-01")
	headAt("T-PU1", uid["lead@pl.test"], "2019-01-07")
	headAt("T-PU2", uid["lead2@pl.test"], "2019-01-07")
	for i, t := range []string{"T-HR", "T-FIN", "T-AUD", "T-IN", "T-SL1", "T-SLS", "T-PLT", "T-APP"} {
		headAt(t, pid(10+i), "2020-01-06")
	}
	// 겸직 조직장: 입고팀장이 출하팀을 겸직하고 조직장도 맡는다.
	inHead := pid(13)
	seed(Command{Kind: KindConcurrentAdd, UserID: inHead, ToUnit: ids["T-OUT"], Effective: "2021-04-01"})
	headAt("T-OUT", inHead, "2021-04-01") // 같은 날이면 겸직 발령이 먼저 등록되어 있다
	// 겸직 4건 더: 인사팀 사원이 재무팀을, 응용개발팀 사원이 플랫폼개발팀을, 영업1팀 사원이 영업지원팀을, 감사팀원이 인사팀을 겸직
	first := func(team string) int64 {
		for i, p := range people {
			if p.unit == team && p.role == contract.RoleStaff {
				return pid(i + 1)
			}
		}
		return 0
	}
	conc := func(team, to string, day contract.Day) {
		u := first(team)
		seed(Command{Kind: KindConcurrentAdd, UserID: u, ToUnit: ids[to], Effective: later(day, AddDays(hireOf(u), 30))})
	}
	conc("T-HR", "T-FIN", "2022-02-01")
	conc("T-APP", "T-PLT", "2022-05-02")
	conc("T-SL1", "T-SLS", "2023-01-02")
	conc("T-AUD", "T-HR", "2023-06-01")
	// 과거 이력: 승진과 전보
	for i, p := range people {
		if p.role != contract.RoleStaff {
			continue
		}
		day, rank := AddDays(p.hire, 730), "대리"
		if i%4 == 1 {
			day, rank = AddDays(p.hire, 1460), "과장"
		}
		// 미래 날짜나 이미 같은 직위면 넣지 않는다(퇴사·정정 시험 대상 p020 이후 일부도 건드리지 않는다).
		if i%4 > 1 || day > rel(-60) || p.rank == rank {
			continue
		}
		seed(Command{Kind: KindRankChange, UserID: pid(i + 1), Rank: rankID[rank], Effective: day})
	}
	// 폐지된 조직: 구매3팀은 2022 년에 신설한 팀원 둘이 일하다 2024-12 에 구매1팀으로 옮기고 폐지한다.
	// (구매3팀 조직 자체는 위 1단계에서 만들어졌으므로 소속을 이 사람들로 채운다.)
	pu3 := []Command{
		{Kind: KindHire, Name: "구매진", Email: "p901@pl.test", Role: contract.RoleStaff, Rank: rankID["사원"], ToUnit: ids["T-PU3"], Effective: "2022-03-02", Phone: "010-5555-0901", Extension: "3901"},
		{Kind: KindHire, Name: "정구매", Email: "p902@pl.test", Role: contract.RoleStaff, Rank: rankID["사원"], ToUnit: ids["T-PU3"], Effective: "2022-03-02", Phone: "010-5555-0902", Extension: "3902"},
	}
	for i := range pu3 {
		pu3[i].Reason, pu3[i].Confirm = "개발용 시드", true
	}
	cmds3 = append(cmds3, pu3...)
	if _, err := batch(cmds3); err != nil {
		return fmt.Errorf("개발용 조직장·이력: %w", err)
	}
	var pu3IDs []int64
	for _, e := range []string{"p901@pl.test", "p902@pl.test"} {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, e).Scan(&id); err != nil {
			return err
		}
		pu3IDs = append(pu3IDs, id)
	}
	var cmds4 []Command
	for _, id := range pu3IDs {
		cmds4 = append(cmds4, Command{Kind: KindTransfer, UserID: id, ToUnit: ids["T-PU1"], Effective: "2024-12-02", Reason: "구매3팀 폐지에 따른 전보", Confirm: true})
	}
	cmds4 = append(cmds4, Command{Kind: KindUnitClose, UnitID: ids["T-PU3"], Effective: "2024-12-31", Reason: "구매3팀 폐지", Confirm: true})
	// 이름에 "구매"가 들어간 사람과 같은 이름 한 쌍(동명이인)
	cmds4 = append(cmds4,
		Command{Kind: KindHire, Name: people[20].name, Email: "p903@pl.test", Role: contract.RoleStaff, Rank: rankID["사원"], ToUnit: ids["T-SL2"], Effective: "2024-03-04", Reason: "개발용 시드", Confirm: true, Phone: "010-5555-0903", Extension: "3903"},
	)
	// 공석 + 직무대행: 영업2팀은 조직장이 없고 영업1팀 팀원이 대행한다(오늘 전후 기간).
	acting := pid(len(people))
	cmds4 = append(cmds4, Command{Kind: KindActingSet, UnitID: ids["T-SL2"], UserID: acting, Effective: rel(-10), EndDay: rel(20), Reason: "영업2팀장 공석 중 대행", Confirm: true})
	// 퇴사자 2명: 하나는 퇴사 뒤 재입사
	left1, left2 := pid(20), pid(31)
	cmds4 = append(cmds4,
		Command{Kind: KindLeave, UserID: left1, LastDay: "2025-12-31", LeaveKind: "자진", Reason: "퇴사 처리", Confirm: true},
		Command{Kind: KindLeave, UserID: left2, LastDay: "2024-06-30", LeaveKind: "계약 만료", Reason: "퇴사 처리", Confirm: true},
		Command{Kind: KindHire, UserID: left2, Rank: rankID["사원"], ToUnit: ids["T-SL1"], Effective: "2025-03-03", Reason: "재입사", Confirm: true},
	)
	// 예약 전보 1건
	reserved := pid(25)
	dest := "T-APP"
	if people[24].unit == dest {
		dest = "T-PLT"
	}
	cmds4 = append(cmds4, Command{Kind: KindTransfer, UserID: reserved, ToUnit: ids[dest], Effective: rel(20)})
	if _, err := batch(cmds4); err != nil {
		return fmt.Errorf("개발용 사례: %w", err)
	}
	// 취소·정정 이력: 잘못 넣은 전보를 취소하고, 다른 잘못 넣은 전보를 정정한다(시스템이 한 일로 남는다).
	wrongDest := "T-OUT"
	if people[25].unit == wrongDest {
		wrongDest = "T-IN"
	}
	wrongRank := "과장"
	if people[26].rank == wrongRank {
		wrongRank = "대리"
	}
	wrong, err := batch([]Command{
		{Kind: KindTransfer, UserID: pid(26), ToUnit: ids[wrongDest], Effective: rel(-40), Reason: "잘못 입력(취소 예정)", Confirm: true},
		{Kind: KindRankChange, UserID: pid(27), Rank: rankID[wrongRank], Effective: rel(-35), Reason: "잘못 입력(정정 예정)", Confirm: true},
	})
	if err != nil {
		return fmt.Errorf("개발용 취소·정정 준비: %w", err)
	}
	if _, err := s.cancelTx(ctx, tx, actor, wrong[0].EventIDs[0], "대상이 달라 취소", true); err != nil {
		return fmt.Errorf("개발용 취소: %w", err)
	}
	orig, err := s.loadEvent(ctx, tx, wrong[1].EventIDs[0])
	if err != nil {
		return err
	}
	fix := Command{Kind: KindRankChange, UserID: pid(27), Rank: rankID["차장"], Effective: rel(-35), Reason: "직위를 차장으로 정정", Confirm: true}
	fixOut, _, err := s.applyCommands(ctx, tx, actor, []Command{fix}, false, &orig, nil)
	if err != nil {
		return fmt.Errorf("개발용 정정: %w", err)
	}
	if err := s.recordCorrect(ctx, tx, actor, orig, fix, fixOut[0]); err != nil {
		return err
	}

	return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "org.projection_apply", TargetType: audit.TargetOrgUnit, TargetID: "all", Result: audit.ResultSuccess,
		Diff:   []audit.DiffItem{{Key: "changed_users", New: fmt.Sprint(len(legacies) + len(people) + 3)}, {Key: "date", New: string(today)}},
		Detail: "개발용 조직 시드"})
}
