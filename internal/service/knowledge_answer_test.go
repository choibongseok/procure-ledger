package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
)

// spyLLM 은 받은 요청을 기록하고 reply 로 답한다.
type spyLLM struct {
	reqs  []llm.Request
	reply func(llm.Request) string
}

func (s *spyLLM) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	s.reqs = append(s.reqs, r)
	return llm.Response{Text: s.reply(r), Model: "spy"}, nil
}

// B47 3단계 AC: 질문하기는 질문자가 열 수 있는 조각만 모델에 보내고, 근거가 없으면 모델을 부르지 않으며, 인용 검증을 통과한 문장만 답한다.
func TestKnowledgeAnswerUsesOnlyVisibleChunks(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	k.completedWithAttachments(t, "비밀 조달 문서", map[string][]byte{"메모.txt": []byte("극비 조달 단가는 칠백팔십 원으로 확정한다")})
	k.drain(t)
	spy := &spyLLM{reply: func(r llm.Request) string {
		return "극비 조달 단가는 칠백팔십 원이다. [1]\n지어낸 문장이다. [5]"
	}}
	ans := &knowledge.Answerer{Search: k.svc, LLM: spy, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}}

	// 열람 범위 밖: 근거가 없으니 모델을 부르지 않고 고정 문구. 극비 문장은 어떤 요청에도 실리지 않는다.
	for name, who := range map[string]int64{"다른 부서 팀장": uLead2, "구매 전체 열람자": uFinance} {
		out, err := ans.Answer(as(who), "극비 조달 단가는 얼마인가")
		if err != nil || out.Found || out.Text != knowledge.NotFoundText || len(out.Sources) != 0 {
			t.Fatalf("%s: %+v %v", name, out, err)
		}
	}
	if len(spy.reqs) != 0 {
		t.Fatalf("근거가 없는데 모델을 불렀다: %d", len(spy.reqs))
	}
	// 열람 가능: 근거 조각이 프롬프트에 실리고, 근거 없는 문장은 답에서 빠진다. 출처는 부모 문서 링크다.
	out, err := ans.Answer(as(uStaff), "극비 조달 단가는 얼마인가")
	if err != nil || !out.Found || out.Text != "극비 조달 단가는 칠백팔십 원이다. [1]" || len(out.Sources) != 1 {
		t.Fatalf("답: %+v %v", out, err)
	}
	if len(spy.reqs) != 1 || spy.reqs[0].Purpose != knowledge.PurposeAnswer || !strings.Contains(spy.reqs[0].Messages[0].Content, "칠백팔십 원") {
		t.Fatalf("요청: %+v", spy.reqs)
	}
	if !strings.Contains(out.Sources[0].Hit.Source, "메모.txt") || !strings.HasPrefix(out.Sources[0].Hit.Link, "/docs/") {
		t.Fatalf("출처: %+v", out.Sources[0])
	}
	// 모든 문장이 근거 검증에서 떨어지면 고정 문구.
	spy.reply = func(llm.Request) string { return "자료에 없는 말이다. [9]" }
	out, err = ans.Answer(as(uStaff), "극비 조달 단가는 얼마인가")
	if err != nil || out.Found || out.Text != knowledge.NotFoundText {
		t.Fatalf("검증 실패: %+v %v", out, err)
	}
	// 감사: 마스킹한 질문·근거 수·인용 문서·결과. 답 본문은 남기지 않는다.
	if !e.rec.Has(knowledge.ActionAnswer, audit.TargetKnowledge, audit.ResultSuccess) {
		t.Fatal("knowledge.answer 감사 없음")
	}
	if v := lastDiff(t, e, knowledge.ActionAnswer, "outcome"); v != "not_found" {
		t.Fatalf("outcome: %q", v)
	}
	if v := lastDiff(t, e, knowledge.ActionAnswer, "dropped"); v != "1" {
		t.Fatalf("dropped: %q", v)
	}
	// 짧은 질문·권한 없음.
	if _, err := ans.Answer(as(uStaff), "가"); err == nil {
		t.Fatal("한 글자 질문이 통과")
	}
	if _, err := ans.Answer(context.Background(), "극비 조달 단가는 얼마인가"); err == nil {
		t.Fatal("로그인 없이 통과")
	}
}
