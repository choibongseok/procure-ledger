package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// 결재 권한을 잃은 위임자의 수임분은 수임자의 결재함에 올라오지 않는다(결재 판정과 같은 자격 함수). 권한이 돌아오면 다시 보인다.
func TestPendingListSkipsDelegationWhenDelegatorLostRole(t *testing.T) {
	e := newEnv(t)
	e.mustDelegate(t, uLead, uHead)
	pr := e.submitted(t, uStaff, 500_000)
	has := func() bool {
		list, err := e.approval.ListPending(as(uHead))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.ID == pr.ID {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("위임 수임분이 결재함에 없음")
	}
	if _, err := e.db.Exec(`UPDATE users SET role = 'staff' WHERE id = ?`, uLead); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("결재 권한을 잃은 위임자의 수임분이 결재함에 남음")
	}
	if _, err := e.db.Exec(`UPDATE users SET role = 'manager' WHERE id = ?`, uLead); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("권한이 돌아왔는데 결재함에 다시 보이지 않음")
	}
}

// personsHookOrg 는 prefetch 의 첫 Persons 조회 때 hook 을 한 번 실행한다(판정 뒤에 무관한 사람의 역할이 바뀌는 경합).
type personsHookOrg struct {
	leftAwareOrg
	once sync.Once
	hook func()
}

func (o *personsHookOrg) Persons(ctx context.Context, ids []int64, asOf contract.Day) (map[int64]contract.OrgPerson, error) {
	o.once.Do(o.hook)
	return o.leftAwareOrg.Persons(ctx, ids, asOf)
}

// 결재 후보 판정 뒤 무관한 사람의 역할이 바뀌어도 결재는 사실을 다시 읽어 재시도하므로 성공한다(곧바로 409 로 거부하지 않는다).
func TestDecideRetriesWhenUnrelatedUserChangesDuringPrefetch(t *testing.T) {
	e := newEnv(t)
	pr := e.submitted(t, uStaff, 500_000)
	ho := &personsHookOrg{leftAwareOrg: leftAwareOrg{e.org}}
	ho.hook = func() {
		if _, err := e.db.Exec(`UPDATE users SET role = 'manager' WHERE id = ?`, uStaff2); err != nil {
			t.Error(err)
		}
	}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: ho, Now: func() time.Time { return e.now }}
	svc := service.NewApprovalService(b)
	if _, err := svc.Decide(as(uLead), pr.ID, domain.Decision{Approve: true}); err != nil {
		t.Fatalf("무관한 변경으로 결재가 거부됨: %v", err)
	}
}
