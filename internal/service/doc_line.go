package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/holders"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 양식 문서의 결재선 만들기(B23 명세 3.6). B21 의 buildLine(조직도 자동 결재선)을 포함하고, 양식의 합의·참조·수신과 기안자 편집을 더한다.
// 상신과 미리보기가 같은 함수를 쓴다. 조직 조회가 있으므로 쓰기 트랜잭션 밖에서만 부른다.

const (
	maxFlowSteps    = 12
	maxParallel     = 5
	maxCCRecipients = 50
	maxReceiveUnits = 10
)

// presetKindsDeps 는 규칙 표 대신 호출자가 정한 직책 종류 목록을 쓰게 하는 lineDeps 다.
type presetKindsDeps struct {
	lineDeps
	kinds []domain.StepKind
}

func (d presetKindsDeps) LineKinds(context.Context, contract.Tx, int64) ([]domain.StepKind, error) {
	return d.kinds, nil
}

func (u lineUser) canAgree() bool { return u.Perms.Has(contract.PermApprovalAgree) }

// docLine 은 만든 결재선과 스냅샷에 저장할 것들이다.
type docLine struct {
	Preview    domain.LinePreview
	Meta       lineMeta
	Steps      []repository.StepSnapshot
	Recipients []domain.DocRecipient
	Authority  *domain.AuthorityNote // B23 단계 3: 전결 판정(스냅샷으로 저장)
}

type docLineArgs struct {
	Form        repository.FormRow
	Def         domain.FormDefinition
	RequesterID int64
	Fields      map[string]any
	Amount      int64
	Edits       []domain.LineEdit
	AsOf        contract.Day
}

func fieldUnitID(fields map[string]any, key string) int64 {
	if n, ok := asInt(fields[key]); ok {
		return n
	}
	return 0
}

// lineEditAllowed 는 양식 line_edit 설정이 이 편집을 허용하는지 사유 문장과 함께 돌려준다.
func lineEditAllowed(mode string, e domain.LineEdit) string {
	switch mode {
	case "none":
		return "이 양식은 결재선을 바꿀 수 없습니다."
	case "add_only":
		switch e.Role {
		case "approve":
			return "이 양식은 결재 단계를 더할 수 없습니다. 합의·참조·수신만 더할 수 있습니다."
		case "remove":
			return "이 양식은 기본 결재선의 단계를 뺄 수 없습니다."
		}
	}
	return ""
}

// buildDocLine 은 양식 문서의 결재선을 만든다. 만들 수 없으면 오류가 아니라 Preview.Complete=false 와 Problems 로 돌려준다.
func (b Base) buildDocLine(ctx context.Context, q contract.Tx, deps lineDeps, a docLineArgs) (docLine, error) {
	var out docLine
	a.Def.Line.Steps = stepsForAmount(a.Def.Line.Steps, a.Amount) // 금액 조건(MinAmount)이 있는 단계는 금액이 닿을 때만 넣는다
	// 1) 직책 단계(결재): 금액 구간 규칙 또는 양식이 정한 직책 목록.
	var kinds []domain.StepKind
	if a.Def.Line.Rule == "amount_tiers" {
		k, err := repository.SQLPurchases{}.LineKindsForForm(ctx, q, a.Form.ID, a.Amount)
		if err != nil {
			return out, err
		}
		kinds = k
	} else {
		for _, st := range a.Def.Line.Steps {
			if st.Role == "approve" && st.Source == "org_level" {
				kinds = append(kinds, domain.StepKind(st.Kind))
			}
		}
	}
	amount := a.Amount
	if amount <= 0 {
		amount = 1 // 금액이 없는 양식은 직책 목록만 쓴다(buildLine 은 금액 0 이하를 거부한다)
	}
	pv, meta, err := buildLine(ctx, q, b.Org, presetKindsDeps{lineDeps: deps, kinds: kinds}, a.RequesterID, amount, a.AsOf)
	if err != nil {
		return out, err
	}
	out.Meta = meta
	out.Preview = pv
	// 직책(org_level) 단계가 없고 조직장·지정 사람 단계만 있는 양식(예: 월 마감 해제 요청서)은 직책 규칙이 비는 것이 정상이다. buildLine 의 "규칙 없음" 문제는 지운다.
	otherSteps := false
	for _, st := range a.Def.Line.Steps {
		if !(st.Role == "approve" && st.Source == "org_level") {
			otherSteps = true
		}
	}
	if len(kinds) == 0 && otherSteps {
		kept := out.Preview.Problems[:0:0]
		for _, pr := range out.Preview.Problems {
			if pr != problemNoRules(amount) {
				kept = append(kept, pr)
			}
		}
		out.Preview.Problems = kept
	}
	problem := func(s string) { // 문제 문구는 여기 한 곳에서만 만들고 같은 문구는 한 번만 담는다
		for _, p := range out.Preview.Problems {
			if p == s {
				return
			}
		}
		out.Preview.Problems = append(out.Preview.Problems, s)
	}
	if len(kinds) == 0 && !otherSteps && len(out.Preview.Problems) == 0 {
		problem("이 양식의 기본 결재선에 결재 단계가 없습니다. 관리자에게 문의하세요.")
	}
	// buildLine 이 조직 사정으로 멈췄으면(요청자 미등록·퇴사) 더 만들지 않는다.
	if meta.UnitID == 0 {
		return finishDocLine(out), nil
	}

	order := 0
	used := map[int64]bool{a.RequesterID: true}
	var steps []domain.LineStep
	for _, st := range pv.Steps {
		if st.Omitted {
			steps = append(steps, st)
			continue
		}
		order++
		st.Order, st.Role, st.Stage, st.Required = order, domain.RoleApprove, order, true
		if st.ApproverID != 0 {
			used[st.ApproverID] = true
		}
		steps = append(steps, st)
	}
	nextStage := order

	// 2) 합의·참조·수신: 양식 고정 단계 → 기안자 편집 순.
	type target struct {
		role, targetType string
		id               int64
		group            int
		required         bool
		source           string
		unitCode         string
		fieldKey         string
		defIdx           int // 양식 기본 결재선 단계의 번호(1부터). 기안자가 더한 것은 0
	}
	// 기안자가 뺀 기본 단계(free 양식, 필수가 아닌 단계만).
	removed := map[int]bool{}
	for _, e := range a.Edits {
		if e.Role == "remove" && lineEditAllowed(a.Def.Settings.LineEdit, e) == "" {
			removed[int(e.TargetID)] = true
		}
	}
	var targets []target
	for di, st := range a.Def.Line.Steps {
		if removed[di+1] {
			switch {
			case st.Required:
				problem("필수 단계는 뺄 수 없습니다.")
			case st.Role == "approve":
				problem("결재 단계는 뺄 수 없습니다.")
			default:
				continue // 필수가 아닌 기본 합의·참조·수신 단계는 기안자가 뺄 수 있다
			}
		}
		switch st.Role {
		case "approve":
			// 직책(org_level) 결재는 위에서 만든 직책 단계가 맡는다. 조직장(unit_head)·지정 사용자(fixed_user)가 맡는 기본 결재 단계는 여기서 대상으로 더한다.
			t := target{role: "approve", required: st.Required, source: st.Source, defIdx: di + 1}
			switch st.Source {
			case "unit_head":
				t.targetType, t.id, t.unitCode = "unit", st.UnitID, st.UnitCode
			case "fixed_user":
				t.targetType, t.id = "user", st.UserID
			default:
				continue
			}
			targets = append(targets, t)
		case "agree":
			t := target{role: "agree", required: st.Required, source: st.Source, group: st.Stage, defIdx: di + 1}
			switch st.Source {
			case "field_unit":
				t.targetType, t.id, t.fieldKey = "unit", fieldUnitID(a.Fields, st.Field), st.Field
			case "unit_head":
				t.targetType, t.id, t.unitCode = "unit", st.UnitID, st.UnitCode
			case "fixed_user":
				t.targetType, t.id = "user", st.UserID
			default:
				continue
			}
			targets = append(targets, t)
		case "receive", "cc":
			t := target{role: st.Role, required: st.Required, source: st.Source, defIdx: di + 1}
			switch st.Source {
			case "field_unit":
				t.targetType, t.id, t.fieldKey = "unit", fieldUnitID(a.Fields, st.Field), st.Field
			case "unit_head", "":
				t.targetType, t.id, t.unitCode = "unit", st.UnitID, st.UnitCode
			default:
				continue
			}
			targets = append(targets, t)
		}
	}
	for _, r := range a.Def.Recipients.CC {
		targets = append(targets, target{role: "cc", targetType: "unit", id: r.UnitID, unitCode: r.UnitCode, source: "default"})
	}
	for _, r := range a.Def.Recipients.Receive {
		targets = append(targets, target{role: "receive", targetType: "unit", id: r.UnitID, unitCode: r.UnitCode, required: false, source: "default"})
	}
	editMode := a.Def.Settings.LineEdit
	for _, e := range a.Edits {
		if why := lineEditAllowed(editMode, e); why != "" {
			problem(why)
			continue
		}
		switch e.Role {
		case "remove":
			continue // 위에서 처리했다
		case "agree", "cc", "receive", "approve":
		default:
			problem(fmt.Sprintf("알 수 없는 결재선 역할입니다(%s).", e.Role))
			continue
		}
		targets = append(targets, target{role: e.Role, targetType: e.TargetType, id: e.TargetID, group: -e.Group, source: "drafter_choice"})
	}

	groupStage := map[int]int{}
	parallelCount := map[int]int{}
	ccSeen := map[string]bool{}
	recvSeen := map[int64]bool{}
	day := a.AsOf
	for _, t := range targets {
		// 조직 코드로 지정된 조직(시드)은 정확 일치로 찾는다.
		if t.targetType == "unit" && t.id == 0 && t.unitCode != "" {
			if u, ok := b.unitByCode(ctx, t.unitCode, day); ok {
				t.id = u.ID
			} else if t.role == "receive" || t.role == "cc" {
				out.Preview.Notes = append(out.Preview.Notes, fmt.Sprintf("조직 코드 %s이(가) 없어 %s 지정을 건너뜁니다.", t.unitCode, roleLabel(t.role)))
				continue
			}
		}
		if t.id == 0 {
			if t.fieldKey != "" {
				lbl := fieldLabel(a.Def, t.fieldKey)
				problem(fmt.Sprintf("「%s」%s 선택해야 결재선을 만들 수 있습니다.", lbl, eulReul(lbl)))
			} else {
				problem("결재선 대상이 지정되지 않았습니다.")
			}
			continue
		}
		if ok, err := b.sameEntityTarget(ctx, q, t.targetType, t.id, day); err != nil {
			problem("결재선 대상을 확인하지 못했습니다.")
			continue
		} else if !ok {
			problem(errDifferentEntity)
			continue
		}
		switch t.role {
		case "agree", "approve":
			if t.role == "approve" && t.targetType != "user" && t.source != "unit_head" {
				problem("결재 단계는 사람으로 지정해야 합니다.")
				continue
			}
			uid, name, unitID, unitName, unitPath, acting, why := b.resolveParticipant(ctx, q, deps, t.targetType, t.id, day)
			if why != "" {
				problem(why)
				continue
			}
			u, uerr := deps.LineUser(ctx, q, uid)
			if uerr != nil {
				if errors.Is(uerr, contract.ErrNotFound) {
					problem(fmt.Sprintf("%s님은 이 시스템 사용자가 아닙니다.", name))
					continue
				}
				return out, uerr
			}
			switch {
			case uid == a.RequesterID:
				problem("기안자는 결재선에 넣을 수 없습니다.")
				continue
			case used[uid]:
				problem(fmt.Sprintf("%s님은 한 문서에서 한 단계만 맡을 수 있습니다.", name))
				continue
			case !u.Active:
				problem(fmt.Sprintf("%s님은 재직 중이 아니어서 결재선에 넣을 수 없습니다.%s", name, editHint(t.source)))
				continue
			case t.role == "agree" && !u.canAgree():
				problem(fmt.Sprintf("%s님에게 합의 권한이 없어 합의자로 넣을 수 없습니다.%s", name, editHint(t.source)))
				continue
			case t.role == "approve" && !u.canDecide():
				problem(fmt.Sprintf("%s님에게 결재 권한이 없어 결재자로 넣을 수 없습니다.%s", name, editHint(t.source)))
				continue
			}
			// 단계 번호: 병렬 묶음(group != 0)은 같은 번호, 아니면 새 번호.
			stage := 0
			if t.role == "agree" && t.group != 0 {
				if s, ok := groupStage[t.group]; ok {
					stage = s
				}
			}
			if stage == 0 {
				nextStage++
				stage = nextStage
				if t.role == "agree" && t.group != 0 {
					groupStage[t.group] = stage
				}
			}
			if t.role == "agree" && t.group != 0 {
				parallelCount[stage]++
				if parallelCount[stage] > maxParallel {
					problem("병렬 합의는 5명까지 지정할 수 있습니다.")
					continue
				}
			}
			used[uid] = true
			order++
			title := "합의"
			kind := domain.StepKind("agree")
			if t.role == "approve" {
				title, kind = "결재", domain.StepKind("approver")
			}
			steps = append(steps, domain.LineStep{Order: order, Kind: kind, Title: title, UnitID: unitID, UnitName: unitName, UnitPath: unitPath,
				ApproverID: uid, ApproverName: name, Acting: acting, Role: t.role, Stage: stage, Required: t.required, DefIndex: t.defIdx})
		case "cc":
			recs, notes := b.resolveCC(ctx, q, deps, t.targetType, t.id, day)
			out.Preview.Notes = append(out.Preview.Notes, notes...)
			for _, r := range recs {
				key := fmt.Sprintf("%s/%d", r.TargetType, r.TargetID)
				if ccSeen[key] {
					continue
				}
				ccSeen[key] = true
				out.Recipients = append(out.Recipients, r)
			}
		case "receive":
			if t.targetType != "unit" {
				problem("수신처는 조직으로 지정해야 합니다.")
				continue
			}
			if recvSeen[t.id] {
				continue
			}
			recvSeen[t.id] = true
			if u, err := b.Org.Unit(ctx, t.id, day); err == nil {
				path, _ := b.Org.Path(ctx, t.id, day)
				out.Recipients = append(out.Recipients, domain.DocRecipient{Kind: "receive", TargetType: "unit", TargetID: u.ID, Name: u.Name, Path: contract.OrgPathText(path)})
			} else {
				problem("수신처 조직을 찾을 수 없습니다.")
			}
		}
	}
	// 참조는 결재·합의 참여자와 기안자를 자동으로 뺀다.
	kept := out.Recipients[:0]
	for _, r := range out.Recipients {
		if r.Kind == "cc" && r.TargetType == "user" && used[r.TargetID] {
			out.Preview.Notes = append(out.Preview.Notes, fmt.Sprintf("%s님은 결재선에 이미 있어 참조에서 뺐습니다.", r.Name))
			continue
		}
		kept = append(kept, r)
	}
	out.Recipients = kept
	flow := 0
	for _, s := range steps {
		if !s.Omitted {
			flow++
		}
	}
	if flow > maxFlowSteps {
		problem(fmt.Sprintf("결재선 단계는 %d개까지 둘 수 있습니다.", maxFlowSteps))
	}
	ccN, recvN := 0, 0
	for _, r := range out.Recipients {
		if r.Kind == "cc" {
			ccN++
		} else {
			recvN++
		}
	}
	if ccN > maxCCRecipients {
		problem(fmt.Sprintf("참조는 %d명까지 지정할 수 있습니다.", maxCCRecipients))
	}
	if recvN > maxReceiveUnits {
		problem(fmt.Sprintf("수신처는 %d곳까지 지정할 수 있습니다.", maxReceiveUnits))
	}
	steps = headApproversFirst(steps)
	if len(out.Preview.Problems) == 0 {
		auth, aerr := applyAuthority(ctx, q, a.Form, a.Def, a.Amount, steps, &out.Preview.Notes)
		if aerr != nil {
			return out, aerr
		}
		out.Authority, out.Preview.Authority = auth, auth
	}
	out.Preview.Steps = steps
	for _, r := range out.Recipients {
		out.Preview.Recipients = append(out.Preview.Recipients, domain.LineRecipient{Kind: r.Kind, TargetType: r.TargetType, TargetID: r.TargetID, Name: r.Name, Path: r.Path, Note: r.Note})
	}
	return finishDocLine(out), nil
}

// editHint 는 기안자가 더한 대상이 문제일 때 무엇을 고쳐야 하는지 알리는 문장이다. 양식이 정한 대상이면 관리자에게 문의하도록 한다.
func editHint(source string) string {
	if source == "drafter_choice" {
		return " 결재선 편집에서 이 사람을 빼거나 다른 사람으로 바꾸세요."
	}
	return " 양식의 기본 결재선 대상이므로 관리자에게 문의하세요."
}

func finishDocLine(d docLine) docLine {
	flow := 0
	for _, s := range d.Preview.Steps {
		if !s.Omitted {
			flow++
		}
	}
	d.Preview.Complete = len(d.Preview.Problems) == 0 && flow > 0
	d.Steps = nil
	if !d.Preview.Complete {
		return d
	}
	for _, st := range d.Preview.Steps {
		if st.Omitted {
			continue
		}
		role := st.Role
		if role == "" {
			role = domain.RoleApprove
		}
		d.Steps = append(d.Steps, repository.StepSnapshot{
			Seq: st.Order, Kind: st.Kind, Role: role, UnitID: st.UnitID, UnitPath: st.UnitPath, Title: st.Title, Acting: st.Acting,
			RaisedNote: st.RaisedNote, LayerNote: st.LayerNote, Assignee: st.ApproverID, AssigneeName: st.ApproverName,
			Stage: st.Stage, Required: st.Required, Source: sourceOf(role, st),
			Skipped: st.Skipped, FinalAuthority: d.Authority != nil && d.Authority.FinalOrder != 0 && d.Authority.FinalOrder == st.Order,
		})
	}
	return d
}

func sourceOf(role string, st domain.LineStep) string {
	if role == domain.RoleAgree {
		return "agree"
	}
	if st.Kind == "approver" {
		return "drafter_choice"
	}
	return "org_level"
}

func roleLabel(role string) string {
	switch role {
	case "agree":
		return "합의"
	case "cc":
		return "참조"
	case "receive":
		return "수신"
	}
	return "결재"
}

func fieldLabel(def domain.FormDefinition, key string) string {
	for _, f := range def.Fields {
		if f.Key == key {
			return f.Label
		}
	}
	return key
}

// unitByCode 는 조직 코드가 정확히 일치하는 조직이다(FindUnits 는 부분 일치라 걸러서 쓴다).
func (b Base) unitByCode(ctx context.Context, code string, day contract.Day) (contract.OrgUnit, bool) {
	if b.Org == nil {
		return contract.OrgUnit{}, false
	}
	list, err := b.Org.FindUnits(ctx, code, day, contract.OrgFindMax)
	if err != nil {
		return contract.OrgUnit{}, false
	}
	for _, u := range list {
		if strings.EqualFold(u.Code, code) {
			return u, true
		}
	}
	return contract.OrgUnit{}, false
}

// resolveParticipant 는 합의·결재 대상(사람 또는 조직)을 사람으로 바꾼다. 조직이면 조직장(직무대행 포함), 공석이면 사유를 돌려준다(올림 없음).
func (b Base) resolveParticipant(ctx context.Context, q contract.Tx, deps lineDeps, targetType string, id int64, day contract.Day) (uid int64, name string, unitID int64, unitName, unitPath string, acting bool, why string) {
	switch targetType {
	case "user":
		u, err := deps.LineUser(ctx, q, id)
		if err != nil {
			return 0, "", 0, "", "", false, "대상 사용자를 찾을 수 없습니다."
		}
		person, perr := b.Org.Person(ctx, id, day)
		if perr == nil {
			if person.OnLeave {
				return 0, "", 0, "", "", false, fmt.Sprintf("%s님은 휴직 중이라 결재선에 넣을 수 없습니다.", u.Name)
			}
			path := person.Primary.Path
			return id, u.Name, person.Primary.Unit.ID, person.Primary.Unit.Name, contract.OrgPathText(path), false, ""
		}
		return id, u.Name, 0, "", "", false, ""
	case "unit":
		unit, err := b.Org.Unit(ctx, id, day)
		if err != nil {
			return 0, "", 0, "", "", false, "대상 조직을 찾을 수 없습니다."
		}
		h, err := b.Org.Head(ctx, id, day)
		if err != nil || h.Vacant {
			return 0, "", 0, "", "", false, fmt.Sprintf("합의 대상 조직 %s의 조직장이 없어 합의자를 정할 수 없습니다.", unit.Name)
		}
		u, err := deps.LineUser(ctx, q, h.UserID)
		if err != nil {
			return 0, "", 0, "", "", false, fmt.Sprintf("%s의 조직장을 시스템 사용자로 찾을 수 없습니다.", unit.Name)
		}
		if hp, perr := b.Org.Person(ctx, h.UserID, day); perr == nil && hp.OnLeave {
			return 0, "", 0, "", "", false, fmt.Sprintf("%s의 조직장 %s님이 휴직 중이라 결재선에 넣을 수 없습니다. 직무대행을 지정하거나 사람을 직접 고르세요.", unit.Name, u.Name)
		}
		path, _ := b.Org.Path(ctx, id, day)
		return h.UserID, u.Name, unit.ID, unit.Name, contract.OrgPathText(path), h.Acting, ""
	}
	return 0, "", 0, "", "", false, "알 수 없는 대상 종류입니다."
}

// resolveCC 는 참조 대상을 저장할 행으로 만든다. 사람은 한 행, 조직은 조직 행과 재직 구성원 행(참조는 구성원 전체가 본다)이다.
func (b Base) resolveCC(ctx context.Context, q contract.Tx, deps lineDeps, targetType string, id int64, day contract.Day) ([]domain.DocRecipient, []string) {
	switch targetType {
	case "user":
		person, err := b.Org.Person(ctx, id, day)
		u, uerr := deps.LineUser(ctx, q, id)
		if err != nil || uerr != nil {
			return nil, []string{"참조 대상 사용자를 조직에서 찾을 수 없어 뺐습니다."}
		}
		if !person.Active || !u.Active {
			return nil, []string{fmt.Sprintf("%s님은 재직 중이 아니어서 참조에서 뺐습니다.", u.Name)}
		}
		return []domain.DocRecipient{{Kind: "cc", TargetType: "user", TargetID: id, Name: u.Name, Path: contract.OrgPathText(person.Primary.Path)}}, nil
	case "unit":
		unit, err := b.Org.Unit(ctx, id, day)
		if err != nil {
			return nil, []string{"참조 조직을 찾을 수 없어 뺐습니다."}
		}
		path, _ := b.Org.Path(ctx, id, day)
		out := []domain.DocRecipient{{Kind: "cc", TargetType: "unit", TargetID: id, Name: unit.Name, Path: contract.OrgPathText(path)}}
		members, err := b.Org.MembersOf(ctx, id, day, true)
		if err == nil {
			persons, perr := b.Org.Persons(ctx, members, day)
			if perr == nil {
				for _, uid := range members {
					p, ok := persons[uid]
					if !ok || !p.Active {
						continue
					}
					u, uerr := deps.LineUser(ctx, q, uid)
					if uerr != nil || !u.Active {
						continue
					}
					out = append(out, domain.DocRecipient{Kind: "cc", TargetType: "user", TargetID: uid, Name: u.Name, Path: contract.OrgPathText(p.Primary.Path), Note: unit.Name + " 소속"})
				}
			}
		}
		return out, nil
	}
	return nil, nil
}

// pruneEdits 는 저장해 둔 결재선 편집에서 지금 결재선에 넣을 수 없는 대상(퇴사·비활성·권한 상실·없어진 조직)을 빼고, 남은 줄에 표시 이름을 채워 돌려준다.
// 뺀 대상마다 안내 문구를 만든다(재상신·개인 결재선 불러오기가 같이 쓴다). 조직 조회가 있으므로 트랜잭션 밖에서만 부른다.
func (b Base) pruneEdits(ctx context.Context, edits []domain.LineEdit) ([]domain.LineEdit, []string) {
	day := contract.DayOf(b.now())
	deps := sqlLineDeps{}
	var kept []domain.LineEdit
	var notes []string
	for _, e := range edits {
		switch e.TargetType {
		case "user":
			u, uerr := deps.LineUser(ctx, b.DB, e.TargetID)
			if uerr != nil {
				notes = append(notes, "더 이상 없는 사용자는 결재선에서 뺐습니다.")
				continue
			}
			why := ""
			switch {
			case !u.Active:
				why = "퇴사했거나 재직 중이 아니어서"
			case e.Role == "agree" && !u.canAgree():
				why = "합의 권한을 잃어서"
			case e.Role == "approve" && !u.canDecide():
				why = "결재 권한을 잃어서"
			}
			if why == "" && b.Org != nil {
				if person, perr := b.Org.Person(ctx, e.TargetID, day); perr != nil || !person.Active {
					why = "조직에서 재직 중이 아니어서"
				} else if person.OnLeave && e.Role != "cc" {
					why = "휴직 중이어서"
				}
			}
			if why != "" {
				notes = append(notes, fmt.Sprintf("%s님은 %s 결재선에서 뺐습니다. 필요하면 다른 사람을 더하세요.", u.Name, why))
				continue
			}
			e.Name = u.Name
		case "unit":
			name := ""
			if b.Org != nil {
				if unit, err := b.Org.Unit(ctx, e.TargetID, day); err == nil {
					name = unit.Name
				}
			}
			if name == "" {
				notes = append(notes, "없어진 조직은 결재선에서 뺐습니다.")
				continue
			}
			e.Name = name
		}
		kept = append(kept, e)
	}
	return kept, notes
}

// purchaseRecipients 는 구매 품의서에 기안자가 더한 참조·수신(명세 1.1 C13)을 저장할 행으로 만든다. 구매 품의서는 결재선을 금액 구간 규칙으로 고정하므로
// 합의·결재 편집은 거부한다. 결재선 참여자와 기안자는 참조에서 자동으로 뺀다. 조직 조회가 있으므로 트랜잭션 밖에서만 부른다.
func (b Base) purchaseRecipients(ctx context.Context, line domain.LinePreview, requesterID int64, edits []domain.LineEdit, day contract.Day) ([]domain.DocRecipient, []string) {
	var recs []domain.DocRecipient
	var problems []string
	if len(edits) == 0 || b.Org == nil {
		return nil, nil
	}
	deps := sqlLineDeps{}
	used := map[int64]bool{requesterID: true}
	for _, st := range line.Steps {
		if st.ApproverID != 0 {
			used[st.ApproverID] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range edits {
		switch e.Role {
		case "cc":
			rs, _ := b.resolveCC(ctx, b.DB, deps, e.TargetType, e.TargetID, day)
			if len(rs) == 0 {
				problems = append(problems, "참조 대상을 결재선에 넣을 수 없습니다. 편집에서 빼거나 다른 사람으로 바꾸세요.")
			}
			for _, r := range rs {
				key := fmt.Sprintf("cc/%s/%d", r.TargetType, r.TargetID)
				if seen[key] || (r.TargetType == "user" && used[r.TargetID]) {
					continue
				}
				seen[key] = true
				recs = append(recs, r)
			}
		case "receive":
			if e.TargetType != "unit" {
				problems = append(problems, "수신처는 조직으로 지정해야 합니다.")
				continue
			}
			key := fmt.Sprintf("receive/%d", e.TargetID)
			if seen[key] {
				continue
			}
			seen[key] = true
			unit, err := b.Org.Unit(ctx, e.TargetID, day)
			if err != nil {
				problems = append(problems, "수신처 조직을 찾을 수 없습니다.")
				continue
			}
			path, _ := b.Org.Path(ctx, e.TargetID, day)
			recs = append(recs, domain.DocRecipient{Kind: "receive", TargetType: "unit", TargetID: unit.ID, Name: unit.Name, Path: contract.OrgPathText(path)})
		default:
			problems = append(problems, "구매 품의서는 참조·수신만 더할 수 있습니다.")
		}
	}
	ccN, recvN := 0, 0
	for _, r := range recs {
		if r.Kind == "cc" {
			ccN++
		} else {
			recvN++
		}
	}
	if ccN > maxCCRecipients {
		problems = append(problems, fmt.Sprintf("참조는 %d명까지 지정할 수 있습니다.", maxCCRecipients))
	}
	if recvN > maxReceiveUnits {
		problems = append(problems, fmt.Sprintf("수신처는 %d곳까지 지정할 수 있습니다.", maxReceiveUnits))
	}
	return recs, problems
}

// eulReul 은 마지막 글자의 받침에 따라 목적격 조사(을/를)를 고른다. 한글이 아니면 "을(를)" 이다.
func eulReul(word string) string {
	r := []rune(word)
	if len(r) == 0 {
		return "을(를)"
	}
	c := r[len(r)-1]
	if c < 0xAC00 || c > 0xD7A3 {
		return "을(를)"
	}
	if (c-0xAC00)%28 == 0 {
		return "를"
	}
	return "을"
}

// headApproversFirst 는 양식의 기본 결재선에 있는 조직장(unit_head) 결재 단계를 직책 단계(재무 등)보다 앞으로 옮기고 번호를 다시 매긴다
// (예: 재고 조정 전표의 "구매팀장 → 재무"). 결재 단계만 있는 결재선에만 적용하고, 합의가 섞여 있으면 기존 순서를 그대로 둔다.
func headApproversFirst(steps []domain.LineStep) []domain.LineStep {
	var head, rest []domain.LineStep
	for _, st := range steps {
		role := st.Role
		if role == "" {
			role = domain.RoleApprove
		}
		if role != domain.RoleApprove {
			return steps
		}
		if st.DefIndex > 0 && st.Kind == "approver" {
			head = append(head, st)
		} else {
			rest = append(rest, st)
		}
	}
	if len(head) == 0 || len(rest) == 0 {
		return steps
	}
	out := append(head, rest...)
	for i := range out {
		if !out[i].Omitted {
			out[i].Order, out[i].Stage = i+1, i+1
		}
	}
	return out
}

// stepsForAmount 는 금액 조건(MinAmount)이 있는 기본 결재 단계를 문서 금액에 맞게 거른다(조건이 없는 단계는 그대로).
func stepsForAmount(steps []domain.StepSpec, amount int64) []domain.StepSpec {
	out := make([]domain.StepSpec, 0, len(steps))
	for _, st := range steps {
		if st.MinAmount > 0 && amount < st.MinAmount {
			continue
		}
		out = append(out, st)
	}
	return out
}

// errDifferentEntity 는 결재선·합의·참조·수신처·위임에 다른 법인 사람이나 조직을 넣으려 할 때의 오류 문구다(B44 11.3).
const errDifferentEntity = "같은 법인 사람만 지정할 수 있습니다."

// sameEntityTarget 은 지정한 사람·조직이 현재 법인 소속인지다. 현재 법인이 없는 context(서버 내부 작업·시험)는 거르지 않는다.
// 겸직자는 법인마다 따로 결재하므로 겸직 법인도 소속이다. 소속 법인이 정해지지 않은 사람은 다른 법인 사람으로 보지 않는다(권한 판정이 따로 막는다).
func (b Base) sameEntityTarget(ctx context.Context, q contract.Tx, targetType string, id int64, day contract.Day) (bool, error) {
	ent := contract.EntityFrom(ctx)
	if ent <= 0 {
		return true, nil
	}
	if targetType == "unit" {
		return holders.UnitIn(ctx, q, id, ent, day)
	}
	return holders.Member(ctx, q, id, ent, day)
}
