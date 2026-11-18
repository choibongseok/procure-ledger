package org

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 조직 CSV 일괄 반영(B22, 6절)의 계획 만들기: 파일 → 이벤트 명령.
// 식별 키는 사번과 조직 코드다. 파일에 없는 사람·조직은 건드리지 않고, 현재 상태와 같은 행은 "변화 없음"이다.

// 파일 종류(헤더로 판별한다).
const (
	ImportUnits        = "units"
	ImportPeople       = "people"
	ImportAppointments = "appointments"
)

var importKindName = map[string]string{ImportUnits: "조직", ImportPeople: "사람", ImportAppointments: "발령"}

// ImportUpload 는 올라온 파일 하나다.
type ImportUpload struct {
	Name string
	Data []byte
}

// ImportInput 은 한 번에 올리는 파일들이다(최대 3개, 종류는 헤더로 판별하므로 칸 이름은 의미가 없다).
type ImportInput struct{ Files []ImportUpload }

// ImportFileInfo 는 읽은 파일의 요약이다.
type ImportFileInfo struct {
	Name     string
	Kind     string // ImportUnits 등
	KindName string // 조직·사람·발령
	Rows     int
	Encoding string
	SHA256   string
}

// ImportRowError 는 행 단위 오류다. Line 이 0 이면 파일 오류다.
type ImportRowError struct {
	File   string // 조직·사람·발령(파일 종류 이름)
	Line   int
	Col    string
	Msg    string
	Header []string
	Raw    []string
}

func (e ImportRowError) String() string {
	switch {
	case e.Line == 0 && (e.File == "조직" || e.File == "사람" || e.File == "발령"):
		return fmt.Sprintf("%s 파일: %s", e.File, e.Msg)
	case e.Line == 0 && (e.File == "" || e.File == "묶음"):
		return e.Msg
	case e.Line == 0:
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	case e.Col == "":
		return fmt.Sprintf("%s %d행: %s", e.File, e.Line, e.Msg)
	}
	return fmt.Sprintf("%s %d행 %s: %s", e.File, e.Line, e.Col, e.Msg)
}

// PlanItem 은 계획의 이벤트 하나다.
type PlanItem struct {
	Cmd     Command
	Class   string // 추가 | 변경 | 퇴사
	Card    string // 건수 카드 이름
	Label   string // 종류 이름
	Target  string // 대상 사람·조직
	Content string
	File    string // 조직·사람·발령
	Line    int
	stage   int
	fileOrd int
	Eff     contract.Day
}

// 건수 카드 이름(명세 7.3 의 5).
const (
	CardUnitCreate = "조직 신설"
	CardUnitChange = "조직 변경"
	CardHire       = "입사"
	CardTransfer   = "전보"
	CardRank       = "직위 변경"
	CardConcurrent = "겸직 변경"
	CardActing     = "직무대행"
	CardLeave      = "퇴사"
	CardNoChange   = "변화 없음"
	CardInfoDiff   = "정보 차이(반영되지 않음)"
)

// ImportCardOrder 는 건수 카드를 보이는 순서다.
var ImportCardOrder = []string{CardUnitCreate, CardUnitChange, CardHire, CardTransfer, CardRank, CardConcurrent, CardActing, CardLeave, CardNoChange, CardInfoDiff}

// ImportPlan 은 파일에서 만든 계획이다.
type ImportPlan struct {
	Base       contract.Day
	Files      []ImportFileInfo
	Items      []PlanItem
	Counts     map[string]int
	NoChange   int
	InfoDiff   int
	Errors     []ImportRowError
	FileErrors []ImportRowError
	// RetroRows 는 효력일이 반영 기준일보다 30일 넘게 앞선 행이다(확인 체크가 필요하다).
	RetroRows []string
	// IgnoredRoleRows 는 기존 사람 행에 권한역할 열이 채워져 있어 무시된 행이다(미리보기 안내).
	IgnoredRoleRows []string
	// RiskyRoleRows 는 위험 권한이 든 역할을 지정하려 한 행 수다(거부 감사용).
	RiskyRoleRows int
	// Warnings 는 오류는 아니지만 알려야 할 것(직원 역할의 조직장 등)이다. 영향 계산(Preview 단계)이 채운다.
	Warnings []string
}

// HasProblems 는 오류가 하나라도 있는지다.
func (p *ImportPlan) HasProblems() bool { return len(p.Errors) > 0 || len(p.FileErrors) > 0 }

var (
	phoneRe     = regexp.MustCompile(`^\+?[0-9-]{3,20}$`)
	empNoRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,40}$`)
	dateDashRe  = regexp.MustCompile(`^(\d{4})[-./](\d{1,2})[-./](\d{1,2})$`)
	dateEightRe = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
)

var importUnitKinds = map[string]contract.OrgKind{
	"회사": contract.OrgCompany, "본부": contract.OrgDivision, "부서": contract.OrgDepartment, "팀": contract.OrgTeam,
}

// parseImportDate 는 YYYY-MM-DD(표준), YYYY.MM.DD, YYYY/MM/DD, YYYY.M.D, YYYYMMDD 를 받아 표준으로 바꾼다.
func parseImportDate(raw string) (contract.Day, string) {
	v := strings.TrimSpace(raw)
	var y, m, d string
	if g := dateDashRe.FindStringSubmatch(v); g != nil {
		y, m, d = g[1], g[2], g[3]
	} else if g := dateEightRe.FindStringSubmatch(v); g != nil {
		y, m, d = g[1], g[2], g[3]
	} else {
		return "", fmt.Sprintf("날짜 형식이 올바르지 않습니다(\"%s\"). 연-월-일(예 2026-12-01)로 적으세요.", v)
	}
	std := fmt.Sprintf("%s-%s-%s", y, pad2(m), pad2(d))
	if _, err := time.Parse("2006-01-02", std); err != nil {
		return "", fmt.Sprintf("%s 은 없는 날짜입니다.", std)
	}
	return contract.Day(std), ""
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

type importFile struct {
	info  ImportFileInfo
	table *CSVTable
}

// planner 는 계획을 만드는 동안 쓰는 상태다.
type planner struct {
	plan *ImportPlan
	svc  *Service
	base contract.Day
	sn   *Snap

	empToUser     map[string]int64
	userEmp       map[int64]string
	userName      map[int64]string
	userMail      map[int64]string
	userPhone     map[int64]string
	userExt       map[int64]string
	userRole      map[int64]string  // 사용자 → 권한을 준 역할 이름들(표시용)
	userDecide    map[int64]bool    // 사용자 → 결재(approval:decide) 권한이 있는지(실효 권한)
	roleKeyByName map[string]string // 역할 이름(기본 템플릿·사용자 정의) → 역할 키
	roleNameByKey map[string]string
	roleDecide    map[string]bool // 역할 키 → 결재 권한이 든 역할인지
	roleRisky     map[string]bool // 역할 키 → 위험 권한이 든 역할인지(가져오기로 지정할 수 없다)
	rankID        map[string]int64
	rankName      map[int64]string
	unitByCde     map[string]UnitSpan     // 기준일에 있는 조직
	allCodes      map[string]bool         // DB 에 있는 모든 조직 코드(기준일에 없는 조직 포함)
	newUnits      map[string]bool         // 이번 파일에서 신설하는 코드
	newEmps       map[string]bool         // 이번 파일에서 입사시키는 사번
	newHire       map[string]contract.Day // 그 사번의 입사일(조직장 보임이 입사보다 앞서지 않게 하는 데 쓴다)
	events        []Event
	cancelled     map[int64]bool
	lastLeave     map[int64]contract.Day // 사람 → 가장 마지막 퇴사 마지막 근무일
}

func (pl *planner) errf(file string, row CSVRow, header []string, col, msg string) {
	pl.plan.Errors = append(pl.plan.Errors, ImportRowError{File: file, Line: row.Line, Col: col, Msg: msg, Header: header, Raw: row.Raw})
}

// text 는 칸 값을 꺼내 길이·제어문자·수식 시작을 검사한다(ok=false 면 오류가 기록되었다).
func (pl *planner) text(file string, row CSVRow, header []string, col string, max int, noFormula bool) (string, bool) {
	v := row.Cells[col]
	if p := cellProblem(v); p != "" {
		pl.errf(file, row, header, col, p)
		return "", false
	}
	if noFormula {
		if f := formulaStart(v); f != "" {
			pl.errf(file, row, header, col, fmt.Sprintf("수식으로 읽힐 수 있는 문자(%s)로 시작해 받을 수 없습니다.", strings.TrimSpace(f)))
			return "", false
		}
	}
	if n := len([]rune(v)); n > max {
		pl.errf(file, row, header, col, fmt.Sprintf("%d자를 넘을 수 없습니다(%d자).", max, n))
		return "", false
	}
	return v, true
}

func (pl *planner) date(file string, row CSVRow, header []string, col string, required bool) (contract.Day, bool) {
	v := row.Cells[col]
	if v == "" {
		if required {
			pl.errf(file, row, header, col, "값이 필요합니다.")
			return "", false
		}
		return "", true
	}
	d, msg := parseImportDate(v)
	if msg != "" {
		pl.errf(file, row, header, col, msg)
		return "", false
	}
	return d, true
}

// BuildImportPlan 은 올라온 파일을 읽고 현재 조직 상태(기준일 base)와 비교해 계획을 만든다. 저장하지 않는다.
func (s *Service) BuildImportPlan(ctx context.Context, in ImportInput, base contract.Day) (*ImportPlan, error) {
	plan := &ImportPlan{Base: base, Counts: map[string]int{}}
	files := map[string]*importFile{}
	for _, up := range in.Files {
		if len(up.Data) == 0 {
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: up.Name, Msg: "빈 파일입니다."})
			continue
		}
		sum := sha256.Sum256(up.Data)
		text, enc, err := DecodeCSVText(up.Data)
		if err != nil {
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: up.Name, Msg: err.Error()})
			continue
		}
		tb, err := ParseCSV(text)
		if err != nil {
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: up.Name, Msg: err.Error()})
			continue
		}
		kind := detectImportKind(tb.Header)
		if kind == "" {
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: up.Name, Msg: "파일 종류를 알 수 없습니다. 제목 줄에 조직코드(조직 파일), 사번·이름(사람 파일), 사번·종류·효력일(발령 파일)이 있어야 합니다."})
			continue
		}
		if prev, dup := files[kind]; dup {
			_ = prev
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: importKindName[kind], Msg: "같은 종류의 파일이 둘 있습니다. 하나만 올리세요."})
			continue
		}
		if missing := missingColumns(kind, tb.Header); len(missing) > 0 {
			plan.FileErrors = append(plan.FileErrors, ImportRowError{File: importKindName[kind], Msg: fmt.Sprintf("필수 열이 없습니다(%s).", strings.Join(missing, ", "))})
			continue
		}
		files[kind] = &importFile{info: ImportFileInfo{Name: up.Name, Kind: kind, KindName: importKindName[kind], Rows: len(tb.Rows), Encoding: enc, SHA256: hex.EncodeToString(sum[:])}, table: tb}
	}
	total := 0
	for _, k := range []string{ImportUnits, ImportPeople, ImportAppointments} {
		if f := files[k]; f != nil {
			plan.Files = append(plan.Files, f.info)
			total += f.info.Rows
		}
	}
	if total > ImportMaxRows {
		plan.FileErrors = append(plan.FileErrors, ImportRowError{File: "묶음", Msg: fmt.Sprintf("행이 5,000개를 넘습니다(%s행). 나누어 올려 주세요.", withComma(total))})
	}
	if len(in.Files) == 0 {
		plan.FileErrors = append(plan.FileErrors, ImportRowError{File: "묶음", Msg: "올릴 파일을 하나 이상 선택하세요."})
	}
	if len(plan.FileErrors) > 0 || len(files) == 0 {
		return plan, nil
	}

	pl, err := s.newPlanner(ctx, plan, base)
	if err != nil {
		return nil, err
	}
	// 이번 묶음에서 새로 생기는 조직 코드·사번을 먼저 모은다(파일 순서와 무관하게 서로 참조할 수 있도록).
	if f := files[ImportUnits]; f != nil {
		for _, row := range f.table.Rows {
			if c := row.Cells["조직코드"]; c != "" && !pl.unitCodeKnown(c) && !pl.allCodes[c] {
				pl.newUnits[c] = true
			}
		}
	}
	if f := files[ImportPeople]; f != nil {
		for _, row := range f.table.Rows {
			if e := row.Cells["사번"]; e != "" {
				if _, exists := pl.empToUser[e]; !exists {
					pl.newEmps[e] = true
					if d, msg := parseImportDate(row.Cells["입사일"]); msg == "" {
						pl.newHire[e] = d
					}
				}
			}
		}
	}
	if f := files[ImportUnits]; f != nil {
		pl.units(f)
	}
	if f := files[ImportPeople]; f != nil {
		pl.people(f)
	}
	if f := files[ImportAppointments]; f != nil {
		pl.appointments(f)
	}
	sort.SliceStable(plan.Items, func(i, j int) bool {
		a, b := plan.Items[i], plan.Items[j]
		if a.Eff != b.Eff {
			return a.Eff < b.Eff
		}
		if a.stage != b.stage {
			return a.stage < b.stage
		}
		if a.fileOrd != b.fileOrd {
			return a.fileOrd < b.fileOrd
		}
		return a.Line < b.Line
	})
	plan.Items = parentsFirst(plan.Items)
	for _, it := range plan.Items {
		plan.Counts[it.Card]++
	}
	plan.Counts[CardNoChange] = plan.NoChange
	plan.Counts[CardInfoDiff] = plan.InfoDiff
	return plan, nil
}

func withComma(n int) string {
	s := fmt.Sprint(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func detectImportKind(header []string) string {
	has := map[string]bool{}
	for _, h := range header {
		has[h] = true
	}
	switch {
	case has["조직코드"]:
		return ImportUnits
	case has["사번"] && has["종류"] && has["효력일"]:
		return ImportAppointments
	case has["사번"] && has["이름"]:
		return ImportPeople
	}
	return ""
}

func missingColumns(kind string, header []string) []string {
	has := map[string]bool{}
	for _, h := range header {
		has[h] = true
	}
	var need []string
	switch kind {
	case ImportUnits:
		need = []string{"조직코드", "조직명", "종류"}
	case ImportPeople:
		need = []string{"사번", "이름", "직위"}
	case ImportAppointments:
		need = []string{"사번", "종류", "효력일"}
	}
	var out []string
	for _, n := range need {
		if !has[n] {
			out = append(out, n)
		}
	}
	return out
}

func (s *Service) newPlanner(ctx context.Context, plan *ImportPlan, base contract.Day) (*planner, error) {
	sn, err := s.snapAt(ctx, base)
	if err != nil {
		return nil, err
	}
	pl := &planner{plan: plan, svc: s, base: base, sn: sn,
		empToUser: map[string]int64{}, userEmp: map[int64]string{}, userName: map[int64]string{}, userMail: map[int64]string{},
		userPhone: map[int64]string{}, userExt: map[int64]string{}, userRole: map[int64]string{}, userDecide: map[int64]bool{}, roleKeyByName: map[string]string{}, roleNameByKey: map[string]string{}, roleDecide: map[string]bool{}, roleRisky: map[string]bool{},
		rankID: map[string]int64{}, rankName: map[int64]string{}, unitByCde: map[string]UnitSpan{}, allCodes: map[string]bool{},
		newUnits: map[string]bool{}, newEmps: map[string]bool{}, newHire: map[string]contract.Day{}, cancelled: map[int64]bool{}, lastLeave: map[int64]contract.Day{}}
	cs, err := s.contacts(ctx, nil)
	if err != nil {
		return nil, err
	}
	for id, c := range cs {
		pl.empToUser[c.empNo], pl.userEmp[id] = id, c.empNo
		pl.userName[id], pl.userMail[id], pl.userPhone[id], pl.userExt[id] = c.name, c.email, c.phone, c.ext
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, COALESCE((SELECT string_agg(n, ',') FROM (SELECT DISTINCT r.name AS n FROM user_effective_permissions e JOIN roles r ON r.id = e.role_id WHERE e.user_id = u.id) d), ''),
		CASE WHEN EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission = 'approval:decide') THEN 1 ELSE 0 END FROM users u`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var names string
		var ok int
		if err := rows.Scan(&id, &names, &ok); err != nil {
			rows.Close()
			return nil, err
		}
		pl.userRole[id], pl.userDecide[id] = strings.ReplaceAll(names, ",", "·"), ok == 1
	}
	rows.Close()
	rlRows, err := s.db.QueryContext(ctx, `SELECT r.key, r.name, CASE WHEN EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_id = r.id AND p.permission = 'approval:decide') THEN 1 ELSE 0 END FROM roles r WHERE r.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rlRows.Next() {
		var key, name string
		var ok int
		if err := rlRows.Scan(&key, &name, &ok); err != nil {
			rlRows.Close()
			return nil, err
		}
		pl.roleKeyByName[name], pl.roleNameByKey[key], pl.roleDecide[key] = key, name, ok == 1
	}
	rlRows.Close()
	for key := range pl.roleNameByKey { // 위험 권한이 든 역할은 가져오기로 지정하지 못한다(access.CheckAssignable 과 같은 규칙, 행 단위 안내용)
		if err := access.CheckAssignable(ctx, s.db, key); err != nil {
			var rr *access.RiskyRoleError
			if errors.As(err, &rr) {
				pl.roleRisky[key] = true
			} else {
				return nil, err
			}
		}
	}
	rrows, err := s.db.QueryContext(ctx, `SELECT id, name FROM org_ranks WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var id int64
		var n string
		if err := rrows.Scan(&id, &n); err != nil {
			rrows.Close()
			return nil, err
		}
		pl.rankID[n], pl.rankName[id] = id, n
	}
	rrows.Close()
	allRanks, _ := s.ranks(ctx)
	for id, n := range allRanks {
		pl.rankName[id] = n
	}
	for _, u := range sn.Units {
		pl.unitByCde[u.Code] = u
	}
	crows, err := s.db.QueryContext(ctx, `SELECT code FROM org_units`)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var c string
		if err := crows.Scan(&c); err != nil {
			crows.Close()
			return nil, err
		}
		pl.allCodes[c] = true
	}
	crows.Close()
	evs, err := LoadEvents(ctx, s.db)
	if err != nil {
		return nil, err
	}
	pl.events = evs
	for _, e := range evs {
		if e.Kind == KindCancel {
			pl.cancelled[e.Cancels] = true
		}
	}
	for _, e := range evs {
		if e.Kind == KindLeave && !pl.cancelled[e.ID] && e.P.LastDay > pl.lastLeave[e.SubjectID] {
			pl.lastLeave[e.SubjectID] = e.P.LastDay
		}
	}
	return pl, nil
}

func (pl *planner) add(it PlanItem) {
	if it.Cmd.Reason == "" && it.Cmd.Effective < pl.base {
		it.Cmd.Reason = "CSV 일괄 반영"
	}
	it.Eff = it.Cmd.Effective
	if it.Cmd.Effective < pl.base && DaysBetween(it.Cmd.Effective, pl.base) > RetroConfirmDays {
		pl.plan.RetroRows = append(pl.plan.RetroRows, fmt.Sprintf("%s %d행, %s", it.File, it.Line, it.Cmd.Effective))
	}
	if it.Cmd.Kind == KindLeave {
		it.Eff = AddDays(it.Cmd.LastDay, 1)
		it.Cmd.Effective = it.Eff
		if it.Cmd.LastDay < pl.base && DaysBetween(it.Cmd.LastDay, pl.base) > RetroConfirmDays {
			pl.plan.RetroRows = append(pl.plan.RetroRows, fmt.Sprintf("%s %d행, %s", it.File, it.Line, it.Cmd.LastDay))
		}
	}
	switch it.Cmd.Kind {
	case KindUnitCreate:
		it.Class, it.Card, it.stage = "추가", CardUnitCreate, 1
	case KindUnitRename, KindUnitMove, KindUnitSort, KindUnitReopen:
		it.Class, it.Card, it.stage = "변경", CardUnitChange, 1
	case KindUnitClose:
		it.Class, it.Card, it.stage = "변경", CardUnitChange, 7
	case KindHeadSet, KindHeadClear:
		it.Class, it.Card, it.stage = "변경", CardUnitChange, 4
	case KindHire:
		it.Class, it.Card, it.stage = "추가", CardHire, 2
	case KindTransfer:
		it.Class, it.Card, it.stage = "변경", CardTransfer, 3
	case KindRankChange:
		it.Class, it.Card, it.stage = "변경", CardRank, 3
	case KindConcurrentAdd:
		it.Class, it.Card, it.stage = "추가", CardConcurrent, 3
	case KindConcurrentEnd:
		it.Class, it.Card, it.stage = "변경", CardConcurrent, 3
	case KindActingSet:
		it.Class, it.Card, it.stage = "추가", CardActing, 5
	case KindLeave:
		it.Class, it.Card, it.stage = "퇴사", CardLeave, 6
	}
	it.Label = KindLabel(it.Cmd.Kind)
	it.fileOrd = map[string]int{"조직": 0, "사람": 1, "발령": 2}[it.File]
	it.Cmd.Source = fmt.Sprintf("%s %d행", it.File, it.Line)
	pl.plan.Items = append(pl.plan.Items, it)
}

func (pl *planner) unitLabel(code string) string {
	if u, ok := pl.unitByCde[code]; ok {
		return fmt.Sprintf("%s(%s)", u.Name, code)
	}
	return code
}

func (pl *planner) personLabel(emp string) string {
	if id, ok := pl.empToUser[emp]; ok {
		return fmt.Sprintf("%s(%s)", pl.userName[id], emp)
	}
	return emp
}

// unitCodeKnown 은 코드가 기준일에 있거나 이번 파일에서 신설되는지다.
func (pl *planner) unitCodeKnown(code string) bool {
	_, ok := pl.unitByCde[code]
	return ok || pl.newUnits[code]
}

func (pl *planner) empKnown(emp string) bool {
	_, ok := pl.empToUser[emp]
	return ok || pl.newEmps[emp]
}

func (pl *planner) rankKnown(name string) bool {
	_, ok := pl.rankID[name]
	return ok
}

func (pl *planner) rankHint() string {
	names := make([]string, 0, len(pl.rankID))
	for n := range pl.rankID {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return pl.rankID[names[i]] < pl.rankID[names[j]] })
	if len(names) > 6 {
		names = append(names[:6], "…")
	}
	return strings.Join(names, "·")
}

// parentsFirst 는 같은 효력일에 신설되는 조직 중 상위 조직이 이번 묶음에서 함께 신설되면 상위가 먼저 오게 한다(코드 참조가 풀리도록).
func parentsFirst(items []PlanItem) []PlanItem {
	for pass := 0; pass < len(items); pass++ {
		moved := false
		for i := range items {
			if items[i].Cmd.Kind != KindUnitCreate || items[i].Cmd.ParentCode == "" {
				continue
			}
			for j := i + 1; j < len(items) && items[j].Eff == items[i].Eff; j++ {
				if items[j].Cmd.Kind == KindUnitCreate && items[j].Cmd.Code == items[i].Cmd.ParentCode {
					it := items[j]
					copy(items[i+1:j+1], items[i:j])
					items[i] = it
					moved = true
					break
				}
			}
			if moved {
				break
			}
		}
		if !moved {
			break
		}
	}
	return items
}
