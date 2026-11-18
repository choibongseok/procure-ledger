package org

import (
	"fmt"
	stdmail "net/mail"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 파일 종류별 행 처리(명세 6.3). 오류는 pl.errf 로 기록하고 계속 읽어 한 번에 모두 보여 준다.

func (pl *planner) units(f *importFile) {
	hd := f.table.Header
	const fn = "조직"
	seen := map[string]int{}
	for _, row := range f.table.Rows {
		code, ok := pl.text(fn, row, hd, "조직코드", 40, true)
		if !ok {
			continue
		}
		if !unitCodeRe.MatchString(code) {
			pl.errf(fn, row, hd, "조직코드", "조직 코드는 영문·숫자·-만 쓸 수 있습니다(40자 이하).")
			continue
		}
		if prev, dup := seen[code]; dup {
			pl.errf(fn, row, hd, "조직코드", fmt.Sprintf("%d행과 조직 코드가 같습니다.", prev))
			continue
		}
		seen[code] = row.Line
		name, ok1 := pl.text(fn, row, hd, "조직명", maxUnitNameLen, true)
		kindStr, ok2 := pl.text(fn, row, hd, "종류", maxOtherLen, true)
		parent, ok3 := pl.text(fn, row, hd, "상위조직코드", 40, true)
		head, ok4 := pl.text(fn, row, hd, "조직장사번", 40, true)
		state, ok5 := pl.text(fn, row, hd, "상태", maxOtherLen, true)
		eff, ok6 := pl.date(fn, row, hd, "효력일", false)
		sortStr, ok7 := pl.text(fn, row, hd, "정렬", maxOtherLen, false)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
			continue
		}
		if name == "" {
			pl.errf(fn, row, hd, "조직명", "값이 필요합니다.")
			continue
		}
		kind, known := importUnitKinds[kindStr]
		if !known {
			pl.errf(fn, row, hd, "종류", fmt.Sprintf("회사·본부·부서·팀 중 하나여야 합니다(\"%s\").", kindStr))
			continue
		}
		if state != "" && state != "사용" && state != "폐지" {
			pl.errf(fn, row, hd, "상태", fmt.Sprintf("사용 또는 폐지여야 합니다(\"%s\").", state))
			continue
		}
		sortVal, hasSort := 0, false
		if sortStr != "" {
			if _, err := fmt.Sscanf(sortStr, "%d", &sortVal); err != nil || fmt.Sprint(sortVal) != sortStr {
				pl.errf(fn, row, hd, "정렬", fmt.Sprintf("정수여야 합니다(\"%s\").", sortStr))
				continue
			}
			hasSort = true
		}
		if parent != "" && !pl.unitCodeKnown(parent) {
			pl.errf(fn, row, hd, "상위조직코드", fmt.Sprintf("없는 조직 코드입니다(\"%s\").", parent))
			continue
		}
		if head != "" && !pl.empKnown(head) {
			pl.errf(fn, row, hd, "조직장사번", fmt.Sprintf("없는 사번입니다(\"%s\").", head))
			continue
		}
		if eff == "" {
			eff = pl.base
		}
		cur, exists := pl.unitByCde[code]
		if !exists {
			if pl.allCodes[code] {
				pl.errf(fn, row, hd, "조직코드", "반영 기준일에는 없는 조직입니다(예약 신설 또는 이미 끝난 조직).")
				continue
			}
			if kind != contract.OrgCompany && parent == "" {
				pl.errf(fn, row, hd, "상위조직코드", "회사 행만 비울 수 있습니다.")
				continue
			}
			if state == "폐지" {
				pl.errf(fn, row, hd, "상태", "새 조직은 폐지 상태로 만들 수 없습니다.")
				continue
			}
			pl.add(PlanItem{File: fn, Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, code),
				Content: fmt.Sprintf("상위 %s, 종류 %s", pl.parentLabel(parent), kindStr),
				Cmd:     Command{Kind: KindUnitCreate, Code: code, Name: name, UnitKind: kind, ParentCode: parent, Sort: sortVal, Effective: eff}})
			if head != "" {
				pl.addHead(row, code, name, head, eff)
			}
			continue
		}
		if cur.Kind != kind {
			pl.errf(fn, row, hd, "종류", "조직 종류는 바꿀 수 없습니다.")
			continue
		}
		changed := false
		target := fmt.Sprintf("%s(%s)", cur.Name, code)
		if cur.Name != name {
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("이름 %s → %s", cur.Name, name),
				Cmd: Command{Kind: KindUnitRename, UnitCode: code, Name: name, Effective: eff}})
		}
		if parent != "" {
			if pu, ok := pl.unitByCde[parent]; !ok || pu.UnitID != cur.ParentID {
				changed = true
				pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("상위 조직 → %s", pl.parentLabel(parent)),
					Cmd: Command{Kind: KindUnitMove, UnitCode: code, ParentCode: parent, Effective: eff}})
			}
		}
		if hasSort && sortVal != cur.Sort {
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("정렬 %d → %d", cur.Sort, sortVal),
				Cmd: Command{Kind: KindUnitSort, UnitCode: code, Sort: sortVal, Effective: eff}})
		}
		if head != "" {
			curHead := ""
			if cur.HeadUser != 0 && !cur.HeadActing {
				curHead = pl.userEmp[cur.HeadUser]
			}
			if curHead != head {
				changed = true
				pl.addHead(row, code, cur.Name, head, eff)
			}
		}
		switch {
		case state == "폐지" && !cur.Closed:
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: "조직 폐지", Cmd: Command{Kind: KindUnitClose, UnitCode: code, Effective: eff}})
		case state == "사용" && cur.Closed:
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: "조직 재개", Cmd: Command{Kind: KindUnitReopen, UnitCode: code, Effective: eff}})
		}
		if !changed {
			pl.plan.NoChange++
		}
	}
}

func (pl *planner) parentLabel(code string) string {
	if code == "" {
		return "(없음)"
	}
	if u, ok := pl.unitByCde[code]; ok {
		return u.Name
	}
	return code
}

func (pl *planner) addHead(row CSVRow, code, name, emp string, eff contract.Day) {
	if h, ok := pl.newHire[emp]; ok && h > eff {
		eff = h // 새로 입사하는 사람은 입사일 이전에 조직장이 될 수 없다
	}
	pl.add(PlanItem{File: "조직", Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, code),
		Content: fmt.Sprintf("조직장 %s 보임", pl.personLabel(emp)),
		Cmd:     Command{Kind: KindHeadSet, UnitCode: code, UserEmp: emp, Effective: eff}})
}

func (pl *planner) people(f *importFile) {
	hd := f.table.Header
	const fn = "사람"
	seen := map[string]int{}
	for _, row := range f.table.Rows {
		emp, ok := pl.text(fn, row, hd, "사번", 40, true)
		if !ok {
			continue
		}
		if !empNoRe.MatchString(emp) {
			pl.errf(fn, row, hd, "사번", "사번은 영문·숫자·-만 쓸 수 있습니다(40자 이하).")
			continue
		}
		if prev, dup := seen[emp]; dup {
			pl.errf(fn, row, hd, "사번", fmt.Sprintf("%d행과 사번이 같습니다.", prev))
			continue
		}
		seen[emp] = row.Line
		name, ok1 := pl.text(fn, row, hd, "이름", maxPersonNameLen, true)
		mail, ok2 := pl.text(fn, row, hd, "이메일", maxOtherLen, true)
		rank, ok3 := pl.text(fn, row, hd, "직위", maxRankLen, true)
		unit, ok4 := pl.text(fn, row, hd, "주소속조직코드", 40, true)
		conc, ok5 := pl.text(fn, row, hd, "겸직조직코드", maxOtherLen*4, true)
		roleStr, ok6 := pl.text(fn, row, hd, "권한역할", maxOtherLen, true)
		phone, ok7 := pl.text(fn, row, hd, "휴대전화", maxOtherLen, false)
		ext, ok8 := pl.text(fn, row, hd, "내선", maxOtherLen, true)
		leaveKind, ok9 := pl.text(fn, row, hd, "퇴사구분", maxOtherLen, true)
		hireDay, ok10 := pl.date(fn, row, hd, "입사일", false)
		leaveDay, ok11 := pl.date(fn, row, hd, "퇴사일", false)
		eff, ok12 := pl.date(fn, row, hd, "효력일", false)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8 && ok9 && ok10 && ok11 && ok12) {
			continue
		}
		if name == "" {
			pl.errf(fn, row, hd, "이름", "값이 필요합니다.")
			continue
		}
		if rank == "" {
			pl.errf(fn, row, hd, "직위", "값이 필요합니다.")
			continue
		}
		if !pl.rankKnown(rank) {
			pl.errf(fn, row, hd, "직위", fmt.Sprintf("등록되지 않은 직위입니다(\"%s\"). 등록된 직위: %s", rank, pl.rankHint()))
			continue
		}
		if phone != "" && !phoneRe.MatchString(phone) {
			pl.errf(fn, row, hd, "휴대전화", fmt.Sprintf("형식이 올바르지 않습니다(\"%s\").", phone))
			continue
		}
		if leaveKind != "" && !leaveKinds[leaveKind] {
			pl.errf(fn, row, hd, "퇴사구분", "자진·권고·정년·계약 만료·기타 중 하나여야 합니다.")
			continue
		}
		var concCodes []string
		bad := false
		for _, c := range strings.Split(conc, ";") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if !pl.unitCodeKnown(c) {
				pl.errf(fn, row, hd, "겸직조직코드", fmt.Sprintf("없는 조직 코드입니다(\"%s\").", c))
				bad = true
				break
			}
			concCodes = append(concCodes, c)
		}
		if bad {
			continue
		}
		if len(concCodes) > MaxConcurrent {
			pl.errf(fn, row, hd, "겸직조직코드", fmt.Sprintf("겸직은 최대 %d개까지 둘 수 있습니다.", MaxConcurrent))
			continue
		}
		if unit != "" && !pl.unitCodeKnown(unit) {
			pl.errf(fn, row, hd, "주소속조직코드", fmt.Sprintf("없는 조직 코드입니다(\"%s\").", unit))
			continue
		}
		uid, exists := pl.empToUser[emp]
		changed, info := false, false
		if !exists {
			// 입사
			if mail == "" {
				pl.errf(fn, row, hd, "이메일", "입사 행에는 값이 필요합니다.")
				continue
			}
			if _, err := stdmail.ParseAddress(mail); err != nil {
				pl.errf(fn, row, hd, "이메일", fmt.Sprintf("형식이 올바르지 않습니다(\"%s\").", mail))
				continue
			}
			if unit == "" {
				pl.errf(fn, row, hd, "주소속조직코드", "입사 행에는 값이 필요합니다.")
				continue
			}
			if hireDay == "" {
				pl.errf(fn, row, hd, "입사일", "신규 행에는 값이 필요합니다.")
				continue
			}
			role := contract.RoleStaff
			if roleStr != "" {
				key, known := pl.roleKeyByName[roleStr]
				if !known {
					pl.errf(fn, row, hd, "권한역할", fmt.Sprintf("등록된 역할 이름(직원·결재자·재무·감사·관리자 또는 역할 관리에서 만든 이름)이어야 합니다(\"%s\").", roleStr))
					continue
				}
				if pl.roleRisky[key] {
					pl.plan.RiskyRoleRows++
					pl.errf(fn, row, hd, "권한역할", fmt.Sprintf("위험 권한이 들어 있는 역할은 가져오기로 지정할 수 없습니다(\"%s\"). 일반 역할로 입사시킨 뒤 역할 관리 화면에서 부여(2인 확인)하세요.", roleStr))
					continue
				}
				role = contract.Role(key)
			}
			pl.add(PlanItem{File: fn, Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, emp),
				Content: fmt.Sprintf("%s 소속, 직위 %s, 권한 역할 %s", pl.parentLabel(unit), rank, pl.roleNameByKey[string(role)]),
				Cmd: Command{Kind: KindHire, Name: name, Email: mail, EmployeeNo: emp, Role: role, RankName: rank, ToUnitCode: unit,
					Phone: phone, Extension: ext, Effective: hireDay}})
			cEff := hireDay
			if eff != "" && eff > cEff {
				cEff = eff
			}
			for _, c := range concCodes {
				pl.add(PlanItem{File: fn, Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, emp), Content: fmt.Sprintf("%s 겸직 발령", pl.parentLabel(c)),
					Cmd: Command{Kind: KindConcurrentAdd, UserEmp: emp, ToUnitCode: c, Effective: cEff}})
			}
			if leaveDay != "" {
				pl.add(PlanItem{File: fn, Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, emp), Content: fmt.Sprintf("마지막 근무일 %s(%s)", DayText(leaveDay), orEtc(leaveKind)),
					Cmd: Command{Kind: KindLeave, UserEmp: emp, LastDay: leaveDay, LeaveKind: orEtc(leaveKind)}})
			}
			continue
		}
		// 기존 사람: 정보 차이. 권한역할 열은 입사 행에만 적용되므로 채워져 있으면 무시된다고 알린다.
		if roleStr != "" {
			pl.plan.IgnoredRoleRows = append(pl.plan.IgnoredRoleRows, fmt.Sprintf("%s(%s): 권한역할 열은 기존 사람에게는 적용되지 않아 무시됩니다. 권한은 역할 관리 화면에서 바꾸세요.", name, emp))
		}
		if pl.userName[uid] != name || (mail != "" && !strings.EqualFold(pl.userMail[uid], mail)) || (phone != "" && pl.userPhone[uid] != phone) || (ext != "" && pl.userExt[uid] != ext) {
			info = true
		}
		ps, inSnap := pl.sn.People[uid]
		if inSnap && ps.Left {
			last := pl.lastLeave[uid]
			switch {
			case hireDay != "" && hireDay > last:
				if unit == "" {
					pl.errf(fn, row, hd, "주소속조직코드", "재입사 행에는 값이 필요합니다.")
					continue
				}
				pl.add(PlanItem{File: fn, Line: row.Line, Target: fmt.Sprintf("%s(%s)", name, emp), Content: fmt.Sprintf("재입사: %s 소속, 직위 %s", pl.parentLabel(unit), rank),
					Cmd: Command{Kind: KindHire, UserID: uid, RankName: rank, ToUnitCode: unit, Effective: hireDay}})
				changed = true
			case leaveDay != "":
				// 퇴사일이 같거나 다르거나 이미 퇴사한 사람은 바뀌는 것이 없다.
			case unit != "":
				pl.errf(fn, row, hd, "", "퇴사한 사람입니다. 재입사는 입사일을 새로 적은 행으로 올리세요.")
				continue
			}
			if !changed {
				if info {
					pl.plan.InfoDiff++
				} else {
					pl.plan.NoChange++
				}
			}
			continue
		}
		if !inSnap {
			pl.errf(fn, row, hd, "사번", "반영 기준일에는 아직 입사하지 않은 사람입니다(예약 입사). 기준일을 지나 다시 올리세요.")
			continue
		}
		if eff == "" {
			eff = pl.base
		}
		target := fmt.Sprintf("%s(%s)", pl.userName[uid], emp)
		if rid := pl.rankID[rank]; rid != ps.Rank {
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("직위 %s → %s", pl.rankName[ps.Rank], rank),
				Cmd: Command{Kind: KindRankChange, UserID: uid, RankName: rank, Effective: eff}})
		}
		if unit != "" {
			if cu, ok := pl.unitByCde[unit]; !ok || cu.UnitID != ps.Primary {
				changed = true
				from := ""
				if pu, ok := pl.sn.Units[ps.Primary]; ok {
					from = pu.Name
				}
				pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("%s → %s", from, pl.parentLabel(unit)),
					Cmd: Command{Kind: KindTransfer, UserID: uid, ToUnitCode: unit, Effective: eff}})
			}
		}
		if _, hasConcCol := row.Cells["겸직조직코드"]; hasConcCol { // 열이 있으면 빈 칸은 "겸직 없음"이다
			have := map[int64]bool{}
			for _, c := range pl.sn.Conc[uid] {
				have[c] = true
			}
			want := map[string]bool{}
			for _, c := range concCodes {
				want[c] = true
			}
			var adds, ends []string
			for _, c := range concCodes {
				if cu, ok := pl.unitByCde[c]; !ok || !have[cu.UnitID] {
					adds = append(adds, c)
				}
			}
			for id := range have {
				u := pl.sn.Units[id]
				if !want[u.Code] {
					ends = append(ends, u.Code)
				}
			}
			sort.Strings(adds)
			sort.Strings(ends)
			for _, c := range ends {
				changed = true
				pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("%s 겸직 해제", pl.parentLabel(c)),
					Cmd: Command{Kind: KindConcurrentEnd, UserID: uid, ToUnitCode: c, Effective: eff}})
			}
			for _, c := range adds {
				changed = true
				pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("%s 겸직 발령", pl.parentLabel(c)),
					Cmd: Command{Kind: KindConcurrentAdd, UserID: uid, ToUnitCode: c, Effective: eff}})
			}
		}
		if leaveDay != "" {
			changed = true
			pl.add(PlanItem{File: fn, Line: row.Line, Target: target, Content: fmt.Sprintf("마지막 근무일 %s(%s)", DayText(leaveDay), orEtc(leaveKind)),
				Cmd: Command{Kind: KindLeave, UserID: uid, LastDay: leaveDay, LeaveKind: orEtc(leaveKind)}})
		}
		switch {
		case changed:
		case info:
			pl.plan.InfoDiff++
		default:
			pl.plan.NoChange++
		}
	}
}

func orEtc(s string) string {
	if s == "" {
		return "기타"
	}
	return s
}

var appointmentKinds = map[string]string{
	"전보": KindTransfer, "직위변경": KindRankChange, "겸직발령": KindConcurrentAdd, "겸직해제": KindConcurrentEnd, "직무대행": KindActingSet, "퇴사": KindLeave,
}

func (pl *planner) appointments(f *importFile) {
	hd := f.table.Header
	const fn = "발령"
	for _, row := range f.table.Rows {
		emp, ok := pl.text(fn, row, hd, "사번", 40, true)
		if !ok {
			continue
		}
		kindStr, ok1 := pl.text(fn, row, hd, "종류", maxOtherLen, true)
		unit, ok2 := pl.text(fn, row, hd, "대상조직코드", 40, true)
		newRank, ok3 := pl.text(fn, row, hd, "새직위", maxRankLen, true)
		leaveKind, ok4 := pl.text(fn, row, hd, "퇴사구분", maxOtherLen, true)
		reason, ok5 := pl.text(fn, row, hd, "사유", maxReasonLen, true)
		eff, ok6 := pl.date(fn, row, hd, "효력일", true)
		actStart, ok7 := pl.date(fn, row, hd, "대행시작일", false)
		actEnd, ok8 := pl.date(fn, row, hd, "대행종료일", false)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8) {
			continue
		}
		kind, known := appointmentKinds[kindStr]
		if !known {
			pl.errf(fn, row, hd, "종류", fmt.Sprintf("전보·직위변경·겸직발령·겸직해제·직무대행·퇴사 중 하나여야 합니다(\"%s\").", kindStr))
			continue
		}
		if !pl.empKnown(emp) {
			pl.errf(fn, row, hd, "사번", fmt.Sprintf("없는 사번입니다(\"%s\").", emp))
			continue
		}
		cmd := Command{Kind: kind, Effective: eff, Reason: reason}
		if uid, ok := pl.empToUser[emp]; ok {
			cmd.UserID = uid
		} else {
			cmd.UserEmp = emp
		}
		needUnit := kind == KindTransfer || kind == KindConcurrentAdd || kind == KindConcurrentEnd || kind == KindActingSet
		if needUnit {
			if unit == "" {
				pl.errf(fn, row, hd, "대상조직코드", "값이 필요합니다.")
				continue
			}
			if !pl.unitCodeKnown(unit) {
				pl.errf(fn, row, hd, "대상조직코드", fmt.Sprintf("없는 조직 코드입니다(\"%s\").", unit))
				continue
			}
			if kind == KindActingSet {
				cmd.UnitCode = unit
			} else {
				cmd.ToUnitCode = unit
			}
		}
		content := ""
		switch kind {
		case KindTransfer:
			content = pl.parentLabel(unit) + "(으)로 전보"
		case KindConcurrentAdd:
			content = pl.parentLabel(unit) + " 겸직 발령"
		case KindConcurrentEnd:
			content = pl.parentLabel(unit) + " 겸직 해제"
		case KindRankChange:
			if newRank == "" || !pl.rankKnown(newRank) {
				msg := "값이 필요합니다."
				if newRank != "" {
					msg = fmt.Sprintf("등록되지 않은 직위입니다(\"%s\"). 등록된 직위: %s", newRank, pl.rankHint())
				}
				pl.errf(fn, row, hd, "새직위", msg)
				continue
			}
			cmd.RankName = newRank
			content = "직위 " + newRank + "(으)로 변경"
		case KindActingSet:
			if actEnd == "" {
				pl.errf(fn, row, hd, "대행종료일", "직무대행에는 값이 필요합니다.")
				continue
			}
			if actStart != "" {
				cmd.Effective = actStart
			}
			cmd.EndDay = actEnd
			content = fmt.Sprintf("%s 직무대행(%s~%s)", pl.parentLabel(unit), DayText(cmd.Effective), DayText(actEnd))
		case KindLeave:
			cmd.LastDay = eff // 퇴사 발령의 효력일 칸은 마지막 근무일이다
			cmd.Effective = ""
			cmd.LeaveKind = orEtc(leaveKind)
			if leaveKind != "" && !leaveKinds[leaveKind] {
				pl.errf(fn, row, hd, "퇴사구분", "자진·권고·정년·계약 만료·기타 중 하나여야 합니다.")
				continue
			}
			content = fmt.Sprintf("마지막 근무일 %s(%s)", DayText(eff), orEtc(leaveKind))
		}
		// 이미 등록된 같은 발령은 변화 없음으로 센다(중복 이벤트 방지, 명세 6.7).
		if pl.duplicateEvent(cmd) {
			pl.plan.NoChange++
			continue
		}
		if eff < pl.base && strings.TrimSpace(reason) == "" {
			pl.errf(fn, row, hd, "사유", "과거 날짜로 등록하려면 사유를 입력해야 합니다.")
			continue
		}
		pl.add(PlanItem{File: fn, Line: row.Line, Target: pl.personLabel(emp), Content: content, Cmd: cmd})
	}
}

// duplicateEvent 는 같은 사람·종류·효력일·내용의 취소되지 않은 이벤트가 이미 있는지다(발령 파일 전용).
func (pl *planner) duplicateEvent(c Command) bool {
	if c.UserID == 0 {
		return false // 같은 파일에서 입사하는 사람의 발령은 아직 이벤트가 없다
	}
	probe := c
	probe.Reason = ""
	switch c.Kind {
	case KindTransfer, KindConcurrentAdd, KindConcurrentEnd:
		if u, ok := pl.unitByCde[c.ToUnitCode]; ok {
			probe.ToUnit = u.UnitID
		} else {
			return false
		}
	case KindActingSet:
		return pl.duplicateActing(c)
	case KindRankChange:
		probe.Rank = pl.rankID[c.RankName]
	case KindLeave:
		probe.Effective = AddDays(c.LastDay, 1)
	}
	want := probe.event(probe.UserID)
	want.Subject = SubjectPerson
	for _, e := range pl.events {
		if e.Kind != want.Kind || pl.cancelled[e.ID] || e.Subject != SubjectPerson || e.SubjectID != want.SubjectID || e.Effective != want.Effective {
			continue
		}
		if e.P.Unit == want.P.Unit && e.P.Rank == want.P.Rank && e.P.LastDay == want.P.LastDay {
			return true
		}
	}
	return false
}

func (pl *planner) duplicateActing(c Command) bool {
	u, ok := pl.unitByCde[c.UnitCode]
	if !ok {
		return false
	}
	for _, e := range pl.events {
		if e.Kind == KindActingSet && !pl.cancelled[e.ID] && e.SubjectID == u.UnitID && e.P.User == c.UserID && e.Effective == c.Effective && e.P.End == c.EndDay {
			return true
		}
	}
	return false
}
