package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/evals"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
	"github.com/choibongseok/procure-ledger/internal/rbac"
)

// noOpener 는 결재 문서가 없는 평가 환경의 열람 판정이다(업로드 문서만 색인한다).
type noOpener struct{}

func (noOpener) Openable(context.Context, []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}
func (noOpener) RelationOpenable(context.Context, []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}
func (noOpener) Recheck(context.Context, int64) (bool, error) { return false, nil }

// runKnowledge 는 질문하기 케이스를 실제 서비스(업로드 공개 범위·인용 검증·감사)와 게이트웨이로 돌려 채점한다.
// 문서는 관리자 경로(Admin.Upload)로 올리고, 질문은 케이스마다 다른 역할·부서로 한다.
func runKnowledge(ctx context.Context, db *sql.DB, txs contract.TxManager, rec *fake.Recorder, gw *llmgw.Gateway, set evals.KnowledgeSet) ([]CaseResult, error) {
	depts := map[string]int64{"구매팀": kit.DeptID, "영업팀": kit.OtherDeptID}
	worker := &knowledge.Worker{Txs: txs}
	admin := &knowledge.Admin{Txs: txs, DB: db, Audit: rec, Worker: worker}
	search := &knowledge.Service{Txs: txs, DB: db, Audit: rec, Masker: rbac.Masker{}, Opener: noOpener{}, Store: knowledge.NewStore(db)}
	ans := &knowledge.Answerer{Search: search, LLM: gw, Txs: txs, Audit: rec, Masker: rbac.Masker{}}
	adminCtx := contract.WithPrincipal(ctx, contract.Principal{UserID: kit.FinanceID, Name: "관리자", Role: contract.RoleAdmin, DepartmentID: kit.DeptID, Channel: contract.ChannelWeb})
	for _, d := range set.Docs {
		if _, err := admin.Upload(adminCtx, domain.KnowledgeUploadInput{Kind: d.Kind, Title: d.Title, Roles: d.Roles, Departments: d.Departments, Filename: d.Filename, Content: []byte(d.Content)}); err != nil {
			return nil, fmt.Errorf("지식 문서 %s: %w", d.Key, err)
		}
	}
	if _, err := worker.Drain(ctx, 50); err != nil {
		return nil, fmt.Errorf("지식 색인: %w", err)
	}
	var out []CaseResult
	for i, c := range set.Cases {
		cr := CaseResult{Suite: "knowledge", ID: c.ID, Critical: c.Critical}
		deptID, ok := depts[c.Dept]
		if !ok {
			return nil, fmt.Errorf("케이스 %s: 알 수 없는 부서 %q", c.ID, c.Dept)
		}
		p := contract.Principal{UserID: int64(500 + i), Name: c.Role, Role: contract.Role(c.Role), DepartmentID: deptID, Channel: contract.ChannelWeb}
		res, err := ans.Answer(contract.WithPrincipal(ctx, p), c.Question)
		if err != nil {
			cr.Detail = "호출 오류: " + err.Error()
			out = append(out, cr)
			continue
		}
		var problems []string
		if res.Found != c.ExpectFound {
			problems = append(problems, fmt.Sprintf("찾음 %v (기대 %v)", res.Found, c.ExpectFound))
		}
		if !res.Found && res.Text != knowledge.NotFoundText {
			problems = append(problems, "찾지 못함 고정 문구가 아님")
		}
		var lines []string
		for _, s := range res.Sources {
			lines = append(lines, s.Hit.Source)
		}
		all := strings.Join(lines, "\n")
		if c.ExpectSource != "" && !strings.Contains(all, c.ExpectSource) {
			problems = append(problems, fmt.Sprintf("출처에 %q 없음", c.ExpectSource))
		}
		if c.ExpectText != "" && !strings.Contains(res.Text, c.ExpectText) {
			problems = append(problems, fmt.Sprintf("답에 %q 없음", c.ExpectText))
		}
		for _, none := range c.ExpectNone {
			if strings.Contains(all, none) {
				problems = append(problems, fmt.Sprintf("볼 수 없는 문서 %q 가 출처에 있음", none))
			}
		}
		for _, no := range c.ExpectNoText {
			if strings.Contains(res.Text, no) {
				problems = append(problems, fmt.Sprintf("답에 있으면 안 되는 글 %q", no))
			}
		}
		cr.Pass = len(problems) == 0
		cr.Detail = fmt.Sprintf("찾음 %v, 출처 %d건", res.Found, len(res.Sources))
		if !cr.Pass {
			cr.Detail = strings.Join(problems, "; ") + " / " + cr.Detail
		}
		out = append(out, cr)
	}
	return out, nil
}
