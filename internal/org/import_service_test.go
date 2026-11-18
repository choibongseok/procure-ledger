package org

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func inputOf(files ...string) ImportInput {
	var in ImportInput
	for i, f := range files {
		in.Files = append(in.Files, ImportUpload{Name: []string{"units.csv", "people.csv", "appts.csv"}[i], Data: []byte(f)})
	}
	return in
}

const importUnitsOK = "조직코드,조직명,종류,상위조직코드,조직장사번\nDATA,데이터팀,팀,DEP,E301\n"
const importPeopleOK = "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE301,한지우,e301@pl.test,사원,DATA,직원,2026-03-10\nE001,김하윤,a@pl.test,과장,T1,,\n"

func TestImportPreviewApplyAndIdempotence(t *testing.T) {
	e, a, _ := importEnv(t)
	in := inputOf(importUnitsOK, importPeopleOK)
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Errors)+len(pv.FileErrors) != 0 || pv.PreviewID == "" || len(pv.Items) != 4 {
		t.Fatalf("미리보기: errors=%v items=%d id=%q", pv.Errors, len(pv.Items), pv.PreviewID)
	}
	// 직원 권한 역할인 사람을 조직장으로 보임하면 경고가 나온다(반영은 가능).
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "권한 역할이 '직원'") {
		t.Errorf("경고: %v", pv.Warnings)
	}
	// 미리보기는 아무것도 저장하지 않고 감사 기록도 남기지 않는다.
	before := e.count(`SELECT COUNT(*) FROM org_events`)
	if pv2, _ := e.svc.PreviewImport(e.ctx, e.admin, in); pv2 == nil || e.count(`SELECT COUNT(*) FROM org_events`) != before || e.count(`SELECT COUNT(*) FROM users WHERE email = 'e301@pl.test'`) != 0 {
		t.Fatal("미리보기가 저장함")
	}
	auditBefore := len(e.auditActions())
	res, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 4 || res.Batch != 1 {
		t.Fatalf("결과 %+v", res)
	}
	// 반영 결과: 새 조직·새 사람·조직장·직위 변경. 이벤트에 출처가 남는다.
	var emp int64
	if err := e.db.QueryRow(`SELECT user_id FROM org_people WHERE employee_no = 'E301'`).Scan(&emp); err != nil {
		t.Fatal(err)
	}
	if _, _, pos := userRow(e, emp); pos != "team_lead" {
		t.Errorf("조직장 보임 투영 %q", pos)
	}
	var src int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE payload LIKE '%"import_batch":1%' AND payload LIKE '%"source":"%행"%'`).Scan(&src)
	if src != 4 {
		t.Errorf("출처가 남은 이벤트 %d건", src)
	}
	_ = a
	// 감사 기록은 한 번의 반영에 1건이다.
	acts := e.auditActions()
	if len(acts) != auditBefore+1 || acts[0] != "org.import_apply" {
		t.Fatalf("감사 %v", acts)
	}
	recs, _ := e.store.List(e.ctx, caudit.Filter{Action: "org.import_apply", Limit: 3})
	if len(recs) != 1 || recs[0].TargetType != caudit.TargetImport || recs[0].TargetID != "1" {
		t.Fatalf("감사 기록 %+v", recs)
	}
	keys := map[string]int{}
	for _, d := range recs[0].Diff {
		keys[d.Key]++
	}
	for _, k := range []string{"file_name", "file_sha256", "rows", "added", "changed", "left", "event_range"} {
		if keys[k] != 1 {
			t.Errorf("diff 키 %s: %v", k, keys)
		}
	}
	if keys["event"] != 4 {
		t.Errorf("상세 이벤트 %d건", keys["event"])
	}
	// 같은 파일을 다시 미리보기 하면 모두 변화 없음이고 반영할 변경이 없다(멱등, 번호도 만들지 않는다).
	pv3, err := e.svc.PreviewImport(e.ctx, e.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if !pv3.NoChangeAll || pv3.PreviewID != "" || len(pv3.Items) != 0 {
		t.Errorf("재반영 미리보기: %+v", pv3)
	}
	if err := Consistent(e.ctx, e.db); err != nil {
		t.Fatalf("정합성: %v", err)
	}
	// 이미 반영된 미리보기 번호는 다시 쓸 수 없다.
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false); err == nil || !strings.Contains(err.Error(), "이미 반영된 미리보기입니다") {
		t.Errorf("재사용: %v", err)
	}
}

func TestImportApplyGuards(t *testing.T) {
	e, _, _ := importEnv(t)
	in := inputOf(importUnitsOK, importPeopleOK)
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	var conflict *ImportConflict
	// 파일이 바뀌었다.
	changed := inputOf(importUnitsOK, strings.Replace(importPeopleOK, "한지우", "한지우2", 1))
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, changed, pv.PreviewID, false); !errors.As(err, &conflict) || !strings.Contains(err.Error(), "파일이 바뀌었습니다") {
		t.Errorf("파일 변경: %v", err)
	}
	// 다른 관리자 번호로는 쓸 수 없다.
	other := e.admin
	other.UserID = 12345
	if _, err := e.svc.ApplyImport(e.ctx, other, in, pv.PreviewID, false); err == nil {
		t.Error("다른 사용자의 미리보기로 반영됨")
	}
	// 변조된 번호.
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID+"x", false); err == nil {
		t.Error("변조된 미리보기 번호로 반영됨")
	}
	// 미리보기 뒤에 조직이 바뀌었다.
	e.reg(Command{Kind: KindUnitRename, UnitID: e.unitByCode("T2"), Name: "구매2팀(개명)", Effective: "2026-03-10"})
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false); !errors.As(err, &conflict) || !strings.Contains(err.Error(), "조직이 바뀌었습니다") {
		t.Errorf("조직 변경: %v", err)
	}
	// 만료.
	pv2, _ := e.svc.PreviewImport(e.ctx, e.admin, in)
	e.now = e.now.Add(11 * time.Minute)
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv2.PreviewID, false); err == nil || !strings.Contains(err.Error(), "만료되었습니다") {
		t.Errorf("만료: %v", err)
	}
	e.now = e.now.Add(-11 * time.Minute)
	// 권한: org:manage 가 없으면 403 계열.
	staff := contract.Principal{UserID: 5, Role: contract.RoleStaff, Channel: contract.ChannelWeb}
	if _, err := e.svc.PreviewImport(e.ctx, staff, in); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("권한 없는 미리보기: %v", err)
	}
	if _, err := e.svc.ApplyImport(e.ctx, staff, in, pv.PreviewID, false); !errors.Is(err, contract.ErrForbidden) {
		t.Errorf("권한 없는 반영: %v", err)
	}
}

func TestImportPreviewMapsBatchFailureToRows(t *testing.T) {
	e, _, _ := importEnv(t)
	// 폐지된 조직으로 전보: 계획은 만들어지지만 일괄 등록 검증(Fold)이 거부한다. 행 번호로 짚어야 한다.
	units := "조직코드,조직명,종류,상위조직코드,상태\nT2,구매2팀,팀,DEP,폐지\n"
	people := "사번,이름,이메일,직위,주소속조직코드\nE001,김하윤,a@pl.test,대리,T2\n"
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, people))
	if err != nil {
		t.Fatal(err)
	}
	if pv.PreviewID != "" || len(pv.Errors) == 0 {
		t.Fatalf("오류가 없음: %+v", pv)
	}
	// 같은 날 순서(전보 → 조직 폐지)에서 폐지가 남은 인원 때문에 거부되므로 폐지 행(조직 파일 2행)이 짚인다.
	if pv.Errors[0].File != "조직" || pv.Errors[0].Line != 2 || !strings.Contains(pv.Errors[0].Msg, "소속 인원이 남아 있어 폐지할 수 없습니다") {
		t.Errorf("행으로 짚지 못함: %s", pv.Errors[0])
	}
	if e.count(`SELECT COUNT(*) FROM org_events WHERE kind = 'unit_close'`) != 0 {
		t.Error("오류 미리보기가 저장함")
	}
}

func TestImportRetroOver30NeedsConfirm(t *testing.T) {
	e, _, _ := importEnv(t)
	people := "사번,이름,이메일,직위,주소속조직코드,입사일\nE700,옛입사,old@pl.test,사원,T1,2025-01-05\n"
	in := inputOf(people)
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, in)
	if err != nil || pv.PreviewID == "" || len(pv.RetroRows) != 1 {
		t.Fatalf("%+v %v", pv, err)
	}
	var nc *ImportNeedConfirm
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false); !errors.As(err, &nc) {
		t.Fatalf("확인 없이 반영됨: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM users WHERE email = 'old@pl.test'`) != 0 {
		t.Fatal("확인 없이 반영한 것이 저장됨")
	}
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, true); err != nil {
		t.Fatal(err)
	}
}

func TestImportAllOrNothing(t *testing.T) {
	e, _, _ := importEnv(t)
	// 미리보기 뒤 감사 기록이 실패하면 반영이 전부 되돌려진다.
	in := inputOf(importUnitsOK, importPeopleOK)
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	events := e.count(`SELECT COUNT(*) FROM org_events`)
	e.svc.audit = failingAudit{}
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false); err == nil {
		t.Fatal("감사가 실패했는데 반영됨")
	}
	e.svc.audit = e.store
	if e.count(`SELECT COUNT(*) FROM org_events`) != events || e.count(`SELECT COUNT(*) FROM users WHERE email = 'e301@pl.test'`) != 0 || e.count(`SELECT COUNT(*) FROM org_units WHERE code = 'DATA'`) != 0 {
		t.Fatal("부분 반영이 남음")
	}
	// 실패한 뒤에도 같은 미리보기로 다시 반영할 수 있다(반영 번호는 성공한 반영에만 쓴다).
	res, err := e.svc.ApplyImport(e.ctx, e.admin, in, pv.PreviewID, false)
	if err != nil || res.Batch != 1 {
		t.Fatalf("재시도: %v %+v", err, res)
	}
}

func TestImportImpactPendingApprovals(t *testing.T) {
	e, _, b := importEnv(t) // 박철수(E002, 결재자)
	e.reg(Command{Kind: KindHeadSet, UnitID: e.unitByCode("T1"), UserID: b, Effective: "2026-03-01", Reason: "시험"})
	if _, err := e.db.Exec(`INSERT INTO vendors (id, name) VALUES (1, '거래처')`); err != nil {
		t.Fatal(err)
	}
	var staff int64
	staff = e.hire("요청자", "r@pl.test", e.unitByCode("T2"), contract.RoleStaff, "2024-01-02")
	now := e.now.UTC().Format("2006-01-02T15:04:05Z")
	for i := 1; i <= 2; i++ {
		if _, err := e.db.Exec(`INSERT INTO purchase_requests (id, no, requester_id, department_id, vendor_id, title, status, total_amount, round, created_at, updated_at)
			VALUES (?, ?, ?, 1, 1, ?, 'in_review', 1000, 1, ?, ?)`, i, "PR-00000"+string(rune('0'+i)), staff, "복사용지 "+string(rune('0'+i)), now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(`INSERT INTO approval_steps (pr_id, round, seq, step_kind, status) VALUES (?, 1, 1, 'team_lead', 'pending')`, i); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (?, 1, 1, ?, 'x')`, i, b); err != nil {
			t.Fatal(err)
		}
	}
	people := "사번,이름,이메일,직위,주소속조직코드,퇴사일,퇴사구분\nE002,박철수,b@pl.test,과장,T1,2026-03-31,자진\n"
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(people))
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Impact) != 1 || pv.Impact[0].Count != 2 || len(pv.Impact[0].Docs) != 2 || !strings.Contains(pv.Impact[0].Who, "E002") || !strings.Contains(pv.Impact[0].Why, "퇴사") {
		t.Fatalf("영향: %+v", pv.Impact)
	}
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "공석이 됩니다") {
		t.Errorf("조직장 공석 경고: %v", pv.Warnings)
	}
}

// B33: 권한역할 열은 기본 템플릿 이름뿐 아니라 역할 관리에서 만든 사용자 정의 역할 이름도 받는다. 입사한 사람은 그 역할의 직접 부여를 받고,
// 결재 권한이 있는 역할이면 조직장 보임 경고가 나오지 않는다. 등록되지 않은 이름은 그 칸의 오류다.
func TestImportAcceptsCustomRoleNames(t *testing.T) {
	e, _, _ := importEnv(t)
	db := e.svc.db
	if _, err := db.Exec(`INSERT INTO roles (key, name, kind, created_at, updated_at) VALUES ('r_9','구매 결재자','custom','a','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission) SELECT id, p FROM roles, (SELECT 'approval:decide' AS p UNION SELECT 'purchase:read') WHERE key='r_9'`); err != nil {
		t.Fatal(err)
	}
	units := "조직코드,조직명,종류,상위조직코드,조직장사번\nDATA,데이터팀,팀,DEP,E301\n"
	people := "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE301,한지우,e301@pl.test,사원,DATA,구매 결재자,2026-03-10\n"
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, people))
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Errors)+len(pv.FileErrors) != 0 {
		t.Fatalf("사용자 정의 역할 이름이 거부됨: %v %v", pv.Errors, pv.FileErrors)
	}
	for _, w := range pv.Warnings {
		if strings.Contains(w, "결재 권한이 없습니다") {
			t.Fatalf("결재 권한이 있는 역할인데 경고: %s", w)
		}
	}
	if _, err := e.svc.ApplyImport(e.ctx, e.admin, inputOf(units, people), pv.PreviewID, false); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE u.email = 'e301@pl.test' AND e.permission = 'approval:decide'`).Scan(&n); err != nil || n != 1 {
		var role string
		var g int
		_ = db.QueryRow(`SELECT role FROM users WHERE email = 'e301@pl.test'`).Scan(&role)
		_ = db.QueryRow(`SELECT COUNT(*) FROM role_grants g JOIN users u ON u.id = g.target_id WHERE u.email = 'e301@pl.test'`).Scan(&g)
		t.Fatalf("입사한 사람의 실효 권한: %d %v (users.role=%q, 부여 %d)", n, err, role, g)
	}
	bad := "사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE302,오류,e302@pl.test,사원,DATA,없는 역할,2026-03-10\n"
	pv2, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(units, bad))
	if err != nil {
		t.Fatal(err)
	}
	if len(pv2.Errors) == 0 || !strings.Contains(pv2.Errors[0].Msg, "등록된 역할 이름") {
		t.Fatalf("등록되지 않은 역할 이름: %+v", pv2.Errors)
	}
}
