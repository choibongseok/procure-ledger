package apitoken

import (
	"context"
	"errors"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/changes"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

func (e *env) twoEntities() {
	e.t.Helper()
	if _, err := e.db.Exec(`INSERT INTO entities (id, code, name, is_default, active, created_at, updated_at) VALUES (2, 'ABC', '계열사', 0, 1, 't', 't')`); err != nil {
		e.t.Fatal(err)
	}
}

// B44 12절: 토큰은 법인에 묶인다. 발급자가 가진 범위보다 넓은 토큰은 만들 수 없고, 토큰의 모든 권한은 그 법인에서만 효력이다.
func TestTokenIsBoundToEntityAndCappedByIssuerScope(t *testing.T) {
	e := newEnv(t, nil)
	e.twoEntities()
	ctx := context.Background()
	// 발급자: 법인 1 에서만 권한이 있다.
	e.issuer.EntityPerms = map[contract.Permission]contract.EntitySet{
		contract.PermAPIManage: contract.NewEntitySet(1), contract.PermInventoryRead: contract.NewEntitySet(1), contract.PermPurchaseRead: contract.NewEntitySet(1),
	}
	e.issuer.OwnEntities = []int64{1}
	_, info, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "기본", Scopes: []string{"items:read"}})
	if err != nil || info.EntityID != 1 || info.EntityCode != "MAIN" {
		t.Fatalf("현재 법인(없으면 기본)에 묶임: %+v %v", info, err)
	}
	if _, _, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "다른 법인", Scopes: []string{"items:read"}, EntityID: 2}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("발급자 범위 밖 법인 토큰은 거부돼야 한다: %v", err)
	}
	if _, _, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "그룹", Scopes: []string{"items:read"}, Group: true}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("그룹 범위가 없으면 그룹 토큰은 거부돼야 한다: %v", err)
	}
	// 그룹 범위가 있는 발급자는 둘 다 된다.
	e.issuer.EntityPerms[contract.PermInventoryRead] = contract.EntitySet{All: true}
	e.issuer.EntityPerms[contract.PermPurchaseRead] = contract.EntitySet{All: true}
	rawA, infoA, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "계열사", Scopes: []string{"items:read"}, EntityID: 2})
	if err != nil || infoA.EntityID != 2 || infoA.EntityCode != "ABC" {
		t.Fatalf("법인 2 토큰: %+v %v", infoA, err)
	}
	rawG, infoG, err := e.svc.Issue(ctx, e.issuer, IssueInput{Name: "그룹", Scopes: []string{"items:read"}, Group: true})
	if err != nil || infoG.EntityID != 0 || infoG.EntityCode != "그룹 전체" {
		t.Fatalf("그룹 토큰: %+v %v", infoG, err)
	}
	e.enable()
	// 토큰으로 인증하면 권한이 그 법인에서만 효력이다.
	c, err := e.auth(rawA, "10.0.0.1")
	if err != nil || c.EntityID != 2 || !c.Principal.CanIn(contract.PermInventoryRead, 2) || c.Principal.CanIn(contract.PermInventoryRead, 1) {
		t.Fatalf("법인 토큰의 효력 범위: %+v %v", c, err)
	}
	g, err := e.auth(rawG, "10.0.0.2")
	if err != nil || g.EntityID != 0 || !g.Principal.CanIn(contract.PermInventoryRead, 1) || !g.Principal.CanIn(contract.PermInventoryRead, 2) {
		t.Fatalf("그룹 토큰의 효력 범위: %+v %v", g, err)
	}
	list, err := e.svc.List(ctx, e.issuer)
	if err != nil || len(list) != 3 {
		t.Fatalf("목록: %v %v", list, err)
	}
}

// 변경 피드는 레코드의 법인을 담고 토큰의 법인 범위로 거른다.
func TestChangeFeedFiltersByEntity(t *testing.T) {
	e := newEnv(t, nil)
	e.twoEntities()
	ctx := context.Background()
	tx := contract.SQLTxManager{DB: e.db}
	if err := tx.WithTx(ctx, func(q contract.Tx) error {
		if err := changes.NoteIn(ctx, q, 1, changes.ResPurchaseOrder, 10, changes.OpCreated, ""); err != nil {
			return err
		}
		if err := changes.NoteIn(ctx, q, 2, changes.ResPurchaseOrder, 20, changes.OpCreated, ""); err != nil {
			return err
		}
		return changes.Note(contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: 2}), q, changes.ResPurchaseOrder, 21, changes.OpCreated, "")
	}); err != nil {
		t.Fatal(err)
	}
	ids := func(scope contract.EntityScope) []int64 {
		var out []int64
		if err := tx.WithTx(ctx, func(q contract.Tx) error {
			evs, _, _, err := changes.AfterIn(ctx, q, 0, 100, nil, scope)
			for _, ev := range evs {
				out = append(out, ev.ObjectID)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := ids(contract.EntityScope{IDs: []int64{1}}); len(got) != 1 || got[0] != 10 {
		t.Fatalf("법인 1: %v", got)
	}
	if got := ids(contract.EntityScope{IDs: []int64{2}}); len(got) != 2 || got[0] != 20 || got[1] != 21 {
		t.Fatalf("법인 2(Note 는 현재 법인을 쓴다): %v", got)
	}
	if got := ids(contract.EntityScope{All: true}); len(got) != 3 {
		t.Fatalf("그룹: %v", got)
	}
}
