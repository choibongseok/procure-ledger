package org

import (
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// importEnv 는 기본 조직(CO > DIV > DEP > T1·T2)과 사번이 있는 두 사람을 만든 환경이다. 시계는 KST 2026-03-10.
func importEnv(t *testing.T) (*env, int64, int64) {
	t.Helper()
	e := newEnv(t)
	e.baseOrg()
	a := e.reg(Command{Kind: KindHire, Name: "김하윤", Email: "a@pl.test", EmployeeNo: "E001", ToUnit: e.unitByCode("T1"), Rank: 2, Role: contract.RoleStaff, Effective: "2024-01-02", Reason: "시험"}).UserID
	b := e.reg(Command{Kind: KindHire, Name: "박철수", Email: "b@pl.test", EmployeeNo: "E002", ToUnit: e.unitByCode("T1"), Rank: 3, Role: contract.RoleManager, Effective: "2024-01-02", Reason: "시험"}).UserID
	return e, a, b
}

func (e *env) unitByCode(code string) int64 {
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM org_units WHERE code = ?`, code).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func plan(t *testing.T, e *env, files ...string) *ImportPlan {
	t.Helper()
	var in ImportInput
	for i, f := range files {
		in.Files = append(in.Files, ImportUpload{Name: []string{"a.csv", "b.csv", "c.csv"}[i], Data: []byte(f)})
	}
	p, err := e.svc.BuildImportPlan(e.ctx, in, "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func errTexts(p *ImportPlan) []string {
	var out []string
	for _, x := range append(append([]ImportRowError{}, p.FileErrors...), p.Errors...) {
		out = append(out, x.String())
	}
	return out
}

func TestImportPlanNewUnitsPeopleAndOrdering(t *testing.T) {
	e, _, _ := importEnv(t)
	units := "조직코드,조직명,종류,상위조직코드,조직장사번,정렬\n" +
		"DATA-T,데이터팀,팀,DATA,E301,\n" + // 상위(DATA)가 파일의 뒤쪽에 있어도 순서가 맞춰진다
		"DATA,데이터부,부서,DIV,,\n" +
		"T2,구매2팀,팀,DEP,,30\n" // 이름 같음, 정렬만 바뀜
	people := "사번,이름,이메일,직위,주소속조직코드,겸직조직코드,권한역할,입사일,퇴사일,퇴사구분,효력일\n" +
		"E301,한지우,e301@pl.test,사원,DATA-T,,결재자,2026-03-10,,,\n" +
		"E001,김하윤,a@pl.test,과장,T2,DEP,,,,,2026-03-10\n" + // 직위 변경 + 전보 + 겸직 발령
		"E002,박철수,b@pl.test,과장,T1,,,,2026-03-31,자진,\n" // 퇴사
	p := plan(t, e, units, people)
	if p.HasProblems() {
		t.Fatalf("오류: %v", errTexts(p))
	}
	kinds := map[string]int{}
	for _, it := range p.Items {
		kinds[it.Cmd.Kind]++
	}
	want := map[string]int{KindUnitCreate: 2, KindUnitSort: 1, KindHeadSet: 1, KindHire: 1, KindRankChange: 1, KindTransfer: 1, KindConcurrentAdd: 1, KindLeave: 1}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%s 이벤트 %d건, 기대 %d (전체 %v)", k, kinds[k], n, kinds)
		}
	}
	// 같은 날 순서: 조직 신설(상위가 먼저) → 입사 → 전보·직위·겸직 → 조직장 보임.
	var order []string
	for _, it := range p.Items {
		if it.Eff == "2026-03-10" {
			order = append(order, it.Cmd.Kind+":"+it.Cmd.Code)
		}
	}
	if order[0] != "unit_create:DATA" || order[1] != "unit_create:DATA-T" {
		t.Errorf("상위 조직이 먼저 신설되지 않음: %v", order)
	}
	last := order[len(order)-1]
	if !strings.HasPrefix(last, "head_set") {
		t.Errorf("조직장 보임은 같은 날 마지막 단계여야 함: %v", order)
	}
	if p.Counts[CardUnitCreate] != 2 || p.Counts[CardHire] != 1 || p.Counts[CardLeave] != 1 || p.Counts[CardTransfer] != 1 {
		t.Errorf("건수 %v", p.Counts)
	}
}

func TestImportPlanNoChangeAndInfoDiff(t *testing.T) {
	e, _, _ := importEnv(t)
	people := "사번,이름,이메일,직위,주소속조직코드\n" +
		"E001,김하윤,a@pl.test,대리,T1\n" + // 현재와 같다
		"E002,박철수 변경,b@pl.test,과장,T1\n" // 이름만 다르다: 정보 차이
	p := plan(t, e, people)
	if p.HasProblems() || len(p.Items) != 0 || p.NoChange != 1 || p.InfoDiff != 1 {
		t.Fatalf("items=%d noChange=%d info=%d errors=%v", len(p.Items), p.NoChange, p.InfoDiff, errTexts(p))
	}
}

func TestImportPlanRowErrors(t *testing.T) {
	e, _, _ := importEnv(t)
	people := "사번,이름,이메일,직위,주소속조직코드,입사일\n" +
		"E001,김하윤,a@pl.test,과장님,T1,\n" + // 직위 오류
		"E001,중복,x@pl.test,사원,T1,2026-03-10\n" + // 사번 중복
		"E500,=SUM(A1),n@pl.test,사원,T1,2026-03-10\n" + // 수식 시작
		"E501,신입,n2@pl.test,사원,NOPE,2026-03-10\n" + // 없는 조직 코드
		"E502,신입2,,사원,T1,2026-03-10\n" + // 이메일 없음
		"E503,신입3,n3@pl.test,사원,T1,26-3-1\n" + // 날짜 형식
		"E504,신입4,not-an-email,사원,T1,2026-03-10\n" // 이메일 형식
	p := plan(t, e, people)
	got := strings.Join(errTexts(p), "\n")
	for _, w := range []string{
		`사람 2행 직위: 등록되지 않은 직위입니다("과장님"). 등록된 직위: 사원·대리·과장`,
		"사람 3행 사번: 2행과 사번이 같습니다.",
		"사람 4행 이름: 수식으로 읽힐 수 있는 문자(=)로 시작해 받을 수 없습니다.",
		`사람 5행 주소속조직코드: 없는 조직 코드입니다("NOPE").`,
		"사람 6행 이메일: 입사 행에는 값이 필요합니다.",
		`사람 7행 입사일: 날짜 형식이 올바르지 않습니다("26-3-1")`,
		`사람 8행 이메일: 형식이 올바르지 않습니다("not-an-email").`, // 다른 오류와 함께 있어도 한 번에 나온다
	} {
		if !strings.Contains(got, w) {
			t.Errorf("오류 문구가 없음: %q\n전체:\n%s", w, got)
		}
	}
	if len(p.Items) != 0 {
		t.Errorf("오류가 있는 행에서 이벤트가 만들어짐: %d", len(p.Items))
	}
}

// 조직 이름에 줄바꿈·제어문자가 있으면 직접 등록(신설·변경)도 거부한다(B20).
func TestUnitNameRejectsControlChars(t *testing.T) {
	e, _, _ := importEnv(t)
	dep := e.unitByCode("DEP")
	for _, bad := range []string{"구매\n3팀", "구매\r3팀", "구매\x013팀", "구매\x7f3팀"} {
		err := e.regErr(Command{Kind: KindUnitCreate, Code: "TX", Name: bad, UnitKind: contract.OrgTeam, ParentID: dep})
		if !strings.Contains(err.Error(), "조직 이름") {
			t.Errorf("신설 %q: 문구 %v", bad, err)
		}
	}
	if err := e.regErr(Command{Kind: KindUnitRename, UnitID: e.unitByCode("T1"), Name: "새\n이름"}); !strings.Contains(err.Error(), "줄바꿈") {
		t.Errorf("변경: %v", err)
	}
}

func TestImportPlanFileLevelErrors(t *testing.T) {
	e, _, _ := importEnv(t)
	if p := plan(t, e); !strings.Contains(strings.Join(errTexts(p), "|"), "올릴 파일을 하나 이상 선택하세요.") {
		t.Errorf("파일 없음: %v", errTexts(p))
	}
	two := "사번,이름,직위\nE9,가,사원\n"
	if p := plan(t, e, two, two); !strings.Contains(strings.Join(errTexts(p), "|"), "같은 종류의 파일이 둘 있습니다") {
		t.Errorf("같은 종류 둘: %v", errTexts(p))
	}
	if p := plan(t, e, "사번,이름\nE9,가\n"); !strings.Contains(strings.Join(errTexts(p), "|"), "사람 파일: 필수 열이 없습니다(직위).") {
		t.Errorf("필수 열: %v", errTexts(p))
	}
	if p := plan(t, e, "가,나\n1,2\n"); !strings.Contains(strings.Join(errTexts(p), "|"), "파일 종류를 알 수 없습니다") {
		t.Errorf("종류 판별: %v", errTexts(p))
	}
}

func TestImportPlanAppointmentsDuplicateAndRetroReason(t *testing.T) {
	e, a, _ := importEnv(t)
	e.reg(Command{Kind: KindTransfer, UserID: a, ToUnit: e.unitByCode("T2"), Effective: "2026-03-01", Reason: "시험"})
	appts := "사번,종류,효력일,대상조직코드,새직위,사유\n" +
		"E001,전보,2026-03-01,T2,,\n" + // 이미 등록된 같은 발령: 변화 없음
		"E001,직위변경,2026-02-01,,과장,\n" + // 소급인데 사유 없음
		"E001,직위변경,2026-04-01,,과장,승진\n" +
		"E002,직무대행,2026-03-15,T1,,\n" // 대행종료일 없음
	p := plan(t, e, appts)
	got := strings.Join(errTexts(p), "\n")
	if p.NoChange != 1 {
		t.Errorf("이미 등록된 발령이 변화 없음으로 세어지지 않음: %d\n%s", p.NoChange, got)
	}
	for _, w := range []string{"발령 3행 사유: 과거 날짜로 등록하려면 사유를 입력해야 합니다.", "발령 5행 대행종료일: 직무대행에는 값이 필요합니다."} {
		if !strings.Contains(got, w) {
			t.Errorf("오류 문구가 없음: %q\n%s", w, got)
		}
	}
	if len(p.Items) != 1 || p.Items[0].Cmd.Kind != KindRankChange || p.Items[0].Cmd.Effective != "2026-04-01" {
		t.Errorf("정상 행만 이벤트가 되어야 함: %+v", p.Items)
	}
}

func TestImportPlanRetroOver30DaysAndDates(t *testing.T) {
	e, _, _ := importEnv(t)
	p := plan(t, e, "사번,이름,이메일,직위,주소속조직코드,입사일\nE600,신입,n@pl.test,사원,T1,2026.1.5\nE601,둘째,m@pl.test,사원,T1,20260309\n")
	if p.HasProblems() {
		t.Fatalf("%v", errTexts(p))
	}
	if len(p.RetroRows) != 1 || !strings.Contains(p.RetroRows[0], "2026-01-05") {
		t.Errorf("30일 넘는 소급 행: %v", p.RetroRows)
	}
	if d, msg := parseImportDate("2026-02-30"); msg == "" || d != "" || !strings.Contains(msg, "없는 날짜") {
		t.Errorf("없는 날짜: %q %q", d, msg)
	}
	for in, want := range map[string]string{"2026-3-1": "2026-03-01", "2026.03.01": "2026-03-01", "2026/3/1": "2026-03-01", "20260301": "2026-03-01"} {
		if d, msg := parseImportDate(in); msg != "" || string(d) != want {
			t.Errorf("%s -> %q %q", in, d, msg)
		}
	}
	for _, bad := range []string{"46082", "03/01/2026", "26-3-1", ""} {
		if _, msg := parseImportDate(bad); msg == "" {
			t.Errorf("%q 는 거부해야 함", bad)
		}
	}
}
