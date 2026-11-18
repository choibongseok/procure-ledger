package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// raceOrg 는 결재선 계산 중 첫 조직 조회 때 hook 을 한 번 실행한다(계산과 상신 트랜잭션 사이에 조직이 바뀌는 경합을 흉내 낸다).
type raceOrg struct {
	leftAwareOrg
	once sync.Once
	hook func()
}

func (o *raceOrg) Person(ctx context.Context, id int64, asOf contract.Day) (contract.OrgPerson, error) {
	o.once.Do(o.hook)
	return o.leftAwareOrg.Person(ctx, id, asOf)
}

// 결재선 계산 뒤 조직장이 퇴사 처리되면 상신 트랜잭션이 이를 알아채 결재선을 다시 만든다. 퇴사자가 배정자로 저장되지 않는다(AC-137).
func TestSubmitRebuildsLineWhenOrgChangesDuringCalculation(t *testing.T) {
	e := newEnv(t)
	d := e.draft(t, uStaff, 500_000)
	ro := &raceOrg{leftAwareOrg: leftAwareOrg{e.org}}
	ro.hook = func() { e.deactivate(t, uLead) }
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: ro, Now: func() time.Time { return e.now }}
	svc := service.NewPurchaseService(b)
	pr, err := svc.Submit(as(uStaff), d.ID)
	if err == nil {
		for _, s := range pr.Steps {
			if s.AssigneeName == "팀장" {
				t.Fatalf("퇴사한 팀장이 배정자로 저장됨: %+v", pr.Steps)
			}
		}
		return
	}
	// 결재선을 새로 만들 수 없으면 한국어 사유와 함께 거부된다. 어느 쪽이든 퇴사자는 배정되지 않았다.
	if n := count(t, e.db, `SELECT COUNT(*) FROM approval_step_assignees WHERE user_id = ?`, uLead); n != 0 {
		t.Fatalf("퇴사자 배정 %d건: %v", n, err)
	}
}
