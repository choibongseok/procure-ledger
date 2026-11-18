package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 조직도 기반 결재선(B21, 3.1). 상신과 미리보기(웹·MCP who_approves)가 같은 buildLine 을 쓴다.

// kindInfo 는 결재 단계 종류의 속성이다. 종류 표(kindRegistry)가 허용하는 종류의 정본이고, DB 는 종류를 닫지 않는다(명세 4.1: B23 이 합의·참조·수신을 더하기 쉽게).
type kindInfo struct {
	Label string // 한국어 직책 이름
	Level int    // 조직장 층(팀 1·부서 2·본부 3·대표 4). 0 이면 조직장 단계가 아니다
	Pool  bool   // 재무처럼 사람이 아니라 역할 풀이 맡는 단계
}

var kindRegistry = map[domain.StepKind]kindInfo{
	domain.StepTeamLead:     {Label: "팀장", Level: contract.OrgTeam.Level()},
	domain.StepDeptHead:     {Label: "부서장", Level: contract.OrgDepartment.Level()},
	domain.StepDivisionHead: {Label: "본부장", Level: contract.OrgDivision.Level()},
	domain.StepCEO:          {Label: "대표", Level: contract.OrgCompany.Level()},
	domain.StepFinance:      {Label: "재무", Pool: true},
}

// stepRoleApprove 는 결재 역할이다. B21 은 이 역할만 만든다(B23 이 합의·참조·수신을 더한다).
const stepRoleApprove = "approve"

func kindOf(k domain.StepKind) (kindInfo, bool) {
	ki, ok := kindRegistry[k]
	return ki, ok
}

// stepKindLabel 은 단계 종류의 한국어 이름이다. 모르는 종류는 "알 수 없음".
func stepKindLabel(k domain.StepKind) string {
	if ki, ok := kindRegistry[k]; ok {
		return ki.Label
	}
	return "알 수 없음"
}

// lineUser 는 결재선 만들기에 필요한 사용자 정보다.
type lineUser struct {
	ID     int64
	Name   string
	Role   contract.Role
	Active bool
	Perms  contract.PermSet // 실효 권한(B33)
}

// canDecide 는 이 사용자가 결재 권한(approval:decide)을 가졌는지다. 권한 판단은 실효 권한(사용자 정의 역할 포함)이 한다.
func (u lineUser) canDecide() bool {
	return u.Perms.Has(contract.PermApprovalDecide)
}

// lineDeps 는 buildLine 이 DB 에서 읽는 것이다. 테스트는 메모리 구현을 넣는다.
type lineDeps interface {
	// LineKinds 는 금액 구간의 결재 단계 종류(규칙 표, seq 순)다.
	LineKinds(ctx context.Context, q contract.Tx, amount int64) ([]domain.StepKind, error)
	// LineUser 는 사용자 한 명이다. 없으면 contract.ErrNotFound.
	LineUser(ctx context.Context, q contract.Tx, id int64) (lineUser, error)
	// FinancePool 은 재직 중인 finance 역할 사용자 전원이다.
	FinancePool(ctx context.Context, q contract.Tx) ([]lineUser, error)
}

// 결재선 불가 문구. 상신 400 본문·미리보기 Problems·who_approves 가 같은 문장을 쓴다(명세 7.1).
const (
	problemNotInOrg = "조직에 등록되지 않아 결재선을 만들 수 없습니다. 관리자에게 인사 발령(입사) 등록을 요청하세요."
	problemLeft     = "퇴사 처리된 사용자는 상신할 수 없습니다."
	problemCEO      = "대표의 구매요청은 결재선을 만들 수 없습니다. 관리자에게 문의하세요."
)

func problemNoRules(amount int64) string {
	return fmt.Sprintf("결재선 규칙이 없습니다(금액 %d원). 관리자에게 문의하세요.", amount)
}

func problemNoAuthority(order int, title, unitPath, name string) string {
	return fmt.Sprintf("%d단계(%s): %s %s %s님에게 결재 권한이 없어 상신할 수 없습니다. 관리자에게 권한 역할 부여를 요청하세요.", order, title, unitPath, title, name)
}

func problemExhausted(order int, title, checked string) string {
	return fmt.Sprintf("%d단계(%s): 위로 올라가도 맡을 결재자를 찾지 못했습니다(%s까지 확인). 관리자에게 조직장 보임을 요청하세요.", order, title, checked)
}

func problemNoFinance(order int) string {
	return fmt.Sprintf("재무 단계(%d단계)를 맡을 재무 담당자가 요청자 외에 없습니다. 관리자에게 결재자 지정을 요청하세요.", order)
}

// hasBatchim 은 문자열의 마지막 글자가 받침 있는 한글인지다. 한글이 아니면 받침이 없는 것으로 본다.
func hasBatchim(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 {
		return false
	}
	r := rs[len(rs)-1]
	return r >= 0xAC00 && r <= 0xD7A3 && (r-0xAC00)%28 != 0
}

// subj 는 직책 이름에 주격 조사를 붙인다(팀장이·부서장이·본부장이·대표가·재무가).
func subj(title string) string {
	if hasBatchim(title) {
		return title + "이"
	}
	return title + "가"
}

// copula 는 직책 이름에 서술격 조사를 붙인다(팀장이라·대표라). 문장은 "…%s 한 단계 위로 올렸습니다" 로 이어 쓴다.
func copula(title string) string {
	if hasBatchim(title) {
		return title + "이라"
	}
	return title + "라"
}

func raiseSentence(unitName, title, reason string) string {
	switch reason {
	case raiseSelf:
		return fmt.Sprintf("요청자 본인이 %s %s 한 단계 위로 올렸습니다.", unitName, copula(title))
	case raiseVacant:
		return fmt.Sprintf("%s %s 공석이라 한 단계 위로 올렸습니다.", unitName, subj(title))
	case raiseSame:
		return fmt.Sprintf("%s %s 앞 단계 결재자와 같은 분이라 한 단계 위로 올렸습니다.", unitName, subj(title))
	case raiseInactive:
		return fmt.Sprintf("%s %s 재직 중이 아니어서 한 단계 위로 올렸습니다.", unitName, subj(title))
	case raiseLeave:
		return fmt.Sprintf("%s %s 휴직 중이어서 한 단계 위로 올렸습니다.", unitName, subj(title))
	}
	return fmt.Sprintf("%s %s에서 한 단계 위로 올렸습니다.", unitName, title)
}

// 올림 사유(LineStep.RaiseReason).
const (
	raiseSelf     = "요청자 본인"
	raiseVacant   = "공석"
	raiseSame     = "앞 단계와 같은 사람"
	raiseInactive = "재직 중이 아님"
	raiseLeave    = "휴직 중"
)

// lineMeta 는 상신 스냅샷에 필요한 요청자 정보다(미리보기 응답에는 나가지 않는다).
type lineMeta struct {
	UnitID int64  // 요청자 주소속 조직 번호
	Rank   string // 요청자 직위 이름
}

// buildLine 은 요청자 requesterID 가 금액 amount 로 상신할 때의 결재선을 기준일 asOf 의 조직으로 만든다.
// 조직 사정으로 만들 수 없으면 오류가 아니라 Complete=false 와 Problems 로 돌려준다.
// 오류는 금액 0 이하(ErrValidation), 없는 사용자(ErrNotFound), 조직 조회 실패뿐이다.
// lineOpts 는 결재선 만들기의 선택 입력이다(B25). ForceFinance 는 예산 초과 때 재무 단계를 강제로 넣는다(상신과 미리보기가 같은 함수를 쓴다).
type lineOpts struct{ ForceFinance bool }

const (
	noteForcedFinance  = "예산 초과로 재무 단계를 더했습니다."
	noteFinanceAlready = "예산 초과: 재무 단계는 이미 포함되어 있습니다."
)

// withForcedFinance 는 단계 종류 목록에 재무 단계가 없으면 끝에 붙인다. 붙였는지(added)를 함께 돌려준다. 목록을 바꾸지 않고 새로 만든다.
func withForcedFinance(kinds []domain.StepKind) (out []domain.StepKind, added bool) {
	for _, k := range kinds {
		if k == domain.StepFinance {
			return kinds, false
		}
	}
	out = append(append([]domain.StepKind{}, kinds...), domain.StepFinance)
	return out, true
}

func buildLine(ctx context.Context, q contract.Tx, org contract.OrgDirectory, deps lineDeps, requesterID, amount int64, asOf contract.Day, optList ...lineOpts) (domain.LinePreview, lineMeta, error) {
	var opt lineOpts
	if len(optList) > 0 {
		opt = optList[0]
	}
	var meta lineMeta
	out := domain.LinePreview{RequesterID: requesterID, AsOf: asOf, Steps: []domain.LineStep{}, Problems: []string{}, Notes: []string{}}
	if amount < 0 {
		return out, meta, fmt.Errorf("%w: 금액은 1원 이상이어야 합니다.", contract.ErrValidation)
	}
	if org == nil {
		return out, meta, errors.New("조직 서비스가 연결되지 않았습니다")
	}
	req, err := deps.LineUser(ctx, q, requesterID)
	if err != nil {
		return out, meta, err
	}
	out.RequesterName = req.Name
	problem := func(s string) { out.Problems = append(out.Problems, s) }
	finish := func() (domain.LinePreview, lineMeta, error) {
		out.Complete = len(out.Problems) == 0 && len(out.Steps) > 0
		return out, meta, nil
	}

	person, err := org.Person(ctx, requesterID, asOf)
	switch {
	case errors.Is(err, contract.ErrNotInOrg):
		problem(problemNotInOrg)
		return finish()
	case err != nil:
		return out, meta, err
	case !person.Active:
		problem(problemLeft)
		return finish()
	}
	path := person.Primary.Path
	meta = lineMeta{UnitID: person.Primary.Unit.ID, Rank: person.Rank}
	out.UnitPath = contract.OrgPathText(path)

	kinds, err := deps.LineKinds(ctx, q, amount)
	if err != nil {
		return out, meta, err
	}
	if len(kinds) == 0 {
		problem(problemNoRules(amount))
		return finish()
	}

	forcedAdded := false
	if opt.ForceFinance {
		var added bool
		kinds, added = withForcedFinance(kinds)
		forcedAdded = added
		if added {
			out.Notes = append(out.Notes, noteForcedFinance)
		} else {
			out.Notes = append(out.Notes, noteFinanceAlready)
		}
	}

	used := map[int64]bool{}
	pos := -1 // 마지막으로 배정된 조직의 사다리 위치(사다리 0 = 요청자 주소속)
	ceoInLine := false
	// 사다리: 주소속에서 회사까지. path 는 회사 → 주소속 순이라 뒤집어서 본다.
	ladder := make([]contract.OrgUnit, len(path))
	for i, u := range path {
		ladder[len(path)-1-i] = u
	}
	pathTo := func(j int) []contract.OrgUnit { return path[:len(path)-j] }
	ceoTried := false

	for _, kind := range kinds {
		ki, ok := kindOf(kind)
		if !ok {
			return out, meta, fmt.Errorf("알 수 없는 결재 단계 종류입니다(%s)", kind)
		}
		n := nonOmitted(out.Steps) + 1 // 이 단계의 표시 번호

		if ki.Pool {
			pool, err := deps.FinancePool(ctx, q)
			if err != nil {
				return out, meta, err
			}
			size := 0
			for _, u := range pool {
				if u.ID != requesterID && !used[u.ID] && u.Active && u.canDecide() {
					size++
				}
			}
			st := domain.LineStep{Order: n, Kind: kind, Title: ki.Label, PoolSize: size, Forced: forcedAdded && kind == domain.StepFinance}
			if size == 0 {
				st.Missing, st.Reason = true, problemNoFinance(n)
				problem(st.Reason)
			}
			out.Steps = append(out.Steps, st)
			continue
		}

		// 직책 단계: 사다리를 위로 올라가며 이 층 이상 조직의 조직장을 찾는다.
		var skipped []domain.LineStep // 올림이 일어난 조직들(사유 문장용)
		var firstRaise domain.LineStep
		found := false
		firstCandidate := true
		layerAbsent := false
		for j := pos + 1; j < len(ladder); j++ {
			u := ladder[j]
			if u.Kind.Level() < ki.Level {
				continue
			}
			if u.Kind == contract.OrgCompany {
				ceoTried = true
			}
			title := contract.HeadTitle(u.Kind)
			if firstCandidate && u.Kind.Level() > ki.Level && len(skipped) == 0 {
				layerAbsent = true
			}
			firstCandidate = false
			h, err := org.Head(ctx, u.ID, asOf)
			if err != nil {
				return out, meta, err
			}
			reason := ""
			var headUser lineUser
			switch {
			case h.Vacant:
				reason = raiseVacant
			case h.UserID == requesterID:
				reason = raiseSelf
			case used[h.UserID]:
				reason = raiseSame
			default:
				hp, perr := org.Person(ctx, h.UserID, asOf)
				if perr != nil && !errors.Is(perr, contract.ErrNotInOrg) && !errors.Is(perr, contract.ErrNotFound) {
					return out, meta, perr
				}
				headUser, err = deps.LineUser(ctx, q, h.UserID)
				if err != nil && !errors.Is(err, contract.ErrNotFound) {
					return out, meta, err
				}
				if perr != nil || !hp.Active || err != nil || !headUser.Active {
					reason = raiseInactive
				} else if hp.OnLeave { // 휴직 중인 조직장은 결재할 수 없으니 올린다(직무대행이 있으면 Head 가 이미 대행자를 돌려준다)
					reason = raiseLeave
				}
			}
			if reason != "" {
				sk := domain.LineStep{RaisedFrom: u.Name, RaiseReason: reason, RaisedNote: raiseSentence(u.Name, title, reason)}
				if len(skipped) == 0 {
					firstRaise = sk
				}
				skipped = append(skipped, sk)
				continue
			}
			unitPath := contract.OrgPathText(pathTo(j))
			st := domain.LineStep{Order: n, Kind: kind, Title: ki.Label, UnitID: u.ID, UnitName: u.Name, UnitPath: unitPath,
				ApproverID: h.UserID, ApproverName: headUser.Name, Acting: h.Acting}
			if len(skipped) > 0 {
				st.RaisedFrom, st.RaiseReason = firstRaise.RaisedFrom, firstRaise.RaiseReason
				notes := make([]string, len(skipped))
				for i, sk := range skipped {
					notes[i] = sk.RaisedNote
				}
				st.RaisedNote = strings.Join(notes, " ")
				out.Notes = append(out.Notes, fmt.Sprintf("%d단계: %s", n, st.RaisedNote))
			}
			if layerAbsent {
				st.LayerNote = fmt.Sprintf("이 조직에는 %s 층이 없어 %s 맡습니다.", ki.Label, subj(title))
				out.Notes = append(out.Notes, fmt.Sprintf("%d단계: %s", n, st.LayerNote))
			}
			if !headUser.canDecide() {
				st.Missing = true
				st.Reason = problemNoAuthority(n, ki.Label, unitPath, headUser.Name)
				problem(st.Reason)
			} else {
				used[h.UserID] = true
				pos = j
				if u.Kind == contract.OrgCompany {
					ceoInLine = true
				}
			}
			out.Steps = append(out.Steps, st)
			found = true
			break
		}
		if found {
			continue
		}
		// 후보를 다 소진했다.
		if ceoInLine {
			st := domain.LineStep{Kind: kind, Title: ki.Label, Omitted: true,
				Reason: fmt.Sprintf("더 올라갈 곳이 없어 %s 단계는 생략합니다(대표가 이미 결재선에 있습니다).", ki.Label)}
			out.Steps = append(out.Steps, st)
			out.Notes = append(out.Notes, st.Reason)
			continue
		}
		st := domain.LineStep{Order: n, Kind: kind, Title: ki.Label, Missing: true}
		if ceoTried && requesterIsCompanyHead(ctx, org, ladder, requesterID, asOf) {
			st.Reason = problemCEO
			if !hasString(out.Problems, problemCEO) {
				problem(problemCEO)
			}
		} else {
			st.Reason = problemExhausted(n, ki.Label, contract.OrgPathText(path))
			problem(st.Reason)
		}
		out.Steps = append(out.Steps, st)
	}
	return finish()
}

func nonOmitted(steps []domain.LineStep) int {
	n := 0
	for _, s := range steps {
		if !s.Omitted {
			n++
		}
	}
	return n
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// requesterIsCompanyHead 는 요청자 본인이 회사(사다리 맨 위)의 조직장인지다(대표 요청 문구용).
func requesterIsCompanyHead(ctx context.Context, org contract.OrgDirectory, ladder []contract.OrgUnit, requesterID int64, asOf contract.Day) bool {
	if len(ladder) == 0 {
		return false
	}
	top := ladder[len(ladder)-1]
	if top.Kind != contract.OrgCompany {
		return false
	}
	h, err := org.Head(ctx, top.ID, asOf)
	return err == nil && !h.Vacant && h.UserID == requesterID
}
