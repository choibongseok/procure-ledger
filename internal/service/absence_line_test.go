package service

import (
	"strings"
	"testing"
)

// B50 8.3 의 2·AC-5033: 조직장이 휴직 중이면 결재선 생성이 "휴직 중" 사유로 한 단계 올린다.
func TestBuildLineHeadOnLeaveIsRaised(t *testing.T) {
	e := newLineEnv()
	e.org.SetOnLeave(lLead, true)
	p := e.build(t, lStaff, 500_000)
	if got := who(p); got != "구매부서장" {
		t.Fatalf("결재선 %q (문제 %v)", got, p.Problems)
	}
	s := p.Steps[0]
	if s.RaiseReason != raiseLeave || s.RaisedFrom != "구매1팀" || !strings.Contains(s.RaisedNote, "구매1팀 팀장이 휴직 중이어서 한 단계 위로 올렸습니다.") {
		t.Fatalf("휴직 올림 %+v", s)
	}
	// 복직하면 원래대로 팀장이 맡는다.
	e.org.SetOnLeave(lLead, false)
	if got := who(e.build(t, lStaff, 500_000)); got != "구매팀장" {
		t.Fatalf("복직 뒤 %q", got)
	}
}

// 조직장이 휴직 중이어도 직무대행이 있으면 조직 조회(Head)가 대행자를 돌려주므로 대행자가 결재선에 선다(조직 쪽 계산은 org 시험이 본다).
func TestBuildLineHeadOnLeaveWithActingKeepsActing(t *testing.T) {
	e := newLineEnv()
	e.org.SetOnLeave(lLead, true)
	e.org.SetHead(6, 0).SetActing(6, lActing) // 휴직 중 조직장의 자리를 대행자가 이어받은 모양
	p := e.build(t, lStaff, 500_000)
	if p.Steps[0].RaiseReason == raiseLeave {
		t.Fatalf("대행자가 있으면 휴직 올림이 아니다: %+v", p.Steps[0])
	}
}
