package sod_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 속성 시험(명세 41 14절): 무작위 순서의 모드 변경·예외 요청·승인·회수·판정·시간 경과 중에
// (a) 잠긴 규칙은 같은 사람이 두 단계를 하려는 판정을 한 번도 통과시키지 않고, 잠긴 규칙의 모드·예외 행이 생기지 않으며,
// (b) 판정이 차단한 업무 변경은 롤백되어 남지 않고(통과한 건수만 남는다), (c) 예외로 통과한 건수는 소진된 예외 수를 넘지 않는다.
func TestPropertyLockedRulesNeverBypassed(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5, 6, 7, 8} {
		seed := seed
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			runProperty(t, seed)
		})
	}
}

func runProperty(t *testing.T, seed int64) {
	e := newEnv(t)
	rng := rand.New(rand.NewSource(seed))
	ctx := context.Background()
	users := []struct {
		id   int64
		role contract.Role
	}{{e.admin, contract.RoleAdmin}, {e.auditor, contract.RoleAuditor}, {e.staff, contract.RoleStaff}, {e.s2, contract.RoleStaff}}
	var txnRules, lockedRules []string
	for _, r := range sod.Rules {
		if r.Kind == sod.KindTxn {
			txnRules = append(txnRules, r.Code)
			if r.Locked {
				lockedRules = append(lockedRules, r.Code)
			}
		}
	}
	allCodes := make([]string, 0, len(sod.Rules))
	for _, r := range sod.Rules {
		allCodes = append(allCodes, r.Code)
	}
	modes := []string{sod.ModeOff, sod.ModeWarn, sod.ModeBlock}
	types := []string{"payment", "journal", "purchase_request", "card_txn"}
	passes, lockedPasses, exceptedPasses := 0, 0, 0
	var exceptionIDs []int64
	type exInfo struct {
		rule, typ  string
		subj, user int64
		role       contract.Role
	}
	var exInfos []exInfo
	for step := 0; step < 300; step++ {
		u := users[rng.Intn(len(users))]
		uctx := e.ctx(u.id, u.role)
		switch rng.Intn(8) {
		case 0:
			_ = e.eng.SetMode(uctx, allCodes[rng.Intn(len(allCodes))], modes[rng.Intn(len(modes))])
		case 1, 2:
			tu := users[rng.Intn(len(users))]
			in := sod.ExceptionInput{Rule: allCodes[rng.Intn(len(allCodes))], SubjectType: types[rng.Intn(len(types))], SubjectID: int64(1 + rng.Intn(4)), UserID: tu.id, Reason: "무작위 시험의 예외 사유입니다"}
			if rng.Intn(2) == 0 { // 관리자가 요청하는 경우를 자주 만든다(요청 권한자)
				uctx = e.ctx(e.admin, contract.RoleAdmin)
			}
			id, err := e.eng.RequestException(uctx, in)
			if err == nil {
				exceptionIDs = append(exceptionIDs, id)
				exInfos = append(exInfos, exInfo{in.Rule, in.SubjectType, in.SubjectID, tu.id, tu.role})
			}
		case 3:
			if len(exceptionIDs) > 0 {
				if rng.Intn(2) == 0 { // 승인 권한자(감사)가 처리하는 경우를 자주 만든다
					uctx = e.ctx(e.auditor, contract.RoleAuditor)
				}
				_ = e.eng.DecideException(uctx, exceptionIDs[rng.Intn(len(exceptionIDs))], rng.Intn(5) != 0, "")
			}
		case 4:
			if len(exceptionIDs) > 0 {
				_ = e.eng.RevokeException(uctx, exceptionIDs[rng.Intn(len(exceptionIDs))])
			}
		case 5:
			*e.clock = e.clock.Add(time.Duration(rng.Intn(60)) * time.Hour)
			if rng.Intn(2) == 0 {
				_, _ = e.eng.Expire(ctx)
			}
		default:
			rule := txnRules[rng.Intn(len(txnRules))]
			other := users[rng.Intn(len(users))].id
			subj := sod.Ref{Type: types[rng.Intn(len(types))], ID: int64(1 + rng.Intn(4)), No: "N"}
			if len(exInfos) > 0 && rng.Intn(10) < 6 { // 만들어 둔 예외와 맞는 판정을 자주 만든다
				x := exInfos[rng.Intn(len(exInfos))]
				rule, subj, u.id, u.role, other = x.rule, sod.Ref{Type: x.typ, ID: x.subj, No: "N"}, x.user, x.role, x.user
				if r, ok := sod.RuleByCode(rule); !ok || r.Kind != sod.KindTxn {
					rule = txnRules[rng.Intn(len(txnRules))]
				}
			}
			same := other == u.id
			key := fmt.Sprintf("prop.%d", step)
			c := sod.Check{Rule: rule, Actor: e.p(u.id, u.role), Subject: subj, Other: other}
			var d sod.Decision
			err := e.txs.WithTx(ctx, func(tx contract.Tx) error {
				// 업무 변경을 먼저 쓰고 판정한다: 차단이면 이 변경이 롤백되어야 한다.
				if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, '1')`, key); err != nil {
					return err
				}
				var derr error
				d, derr = e.eng.Evaluate(ctx, tx, c)
				return derr
			})
			sod.NoteBlocked(ctx, e.eng, err)
			if err != nil && !errors.Is(err, contract.ErrForbidden) {
				t.Fatalf("seed %d step %d: 예상 밖 오류 %v", seed, step, err)
			}
			if err == nil {
				passes++
				if d.Excepted {
					exceptedPasses++
				}
				if same {
					for _, lc := range lockedRules {
						if lc == rule {
							lockedPasses++
						}
					}
				}
			}
		}
	}
	if lockedPasses != 0 {
		t.Fatalf("seed %d: 잠긴 규칙이 %d번 우회됨", seed, lockedPasses)
	}
	if n := e.count(`SELECT COUNT(*) FROM app_settings WHERE key LIKE 'prop.%'`); n != passes {
		t.Fatalf("seed %d: 통과한 판정 %d건인데 업무 변경 %d건이 남음(차단된 변경은 롤백되어야 한다)", seed, passes, n)
	}
	for _, lc := range lockedRules {
		if e.count(`SELECT COUNT(*) FROM sod_rule_settings WHERE rule_code = ?`, lc) != 0 || e.count(`SELECT COUNT(*) FROM sod_exceptions WHERE rule_code = ?`, lc) != 0 ||
			e.count(`SELECT COUNT(*) FROM sod_violations WHERE rule_code = ? AND result <> 'blocked'`, lc) != 0 {
			t.Fatalf("seed %d: 잠긴 규칙 %s 에 설정·예외·비차단 기록이 생김", seed, lc)
		}
	}
	if used := e.count(`SELECT COUNT(*) FROM sod_exceptions WHERE status = 'used'`); exceptedPasses != used || e.count(`SELECT COUNT(*) FROM sod_violations WHERE result = 'excepted'`) != exceptedPasses {
		t.Fatalf("seed %d: 예외 통과 %d건과 소진 예외 %d건이 다름", seed, exceptedPasses, used)
	}
	// 차단 기록 수 = 판정이 차단한 건수(롤백 밖 기록은 시도마다 정확히 한 건).
	blocked := e.count(`SELECT COUNT(*) FROM sod_violations WHERE result = 'blocked'`)
	t.Logf("seed %d: 통과 %d(예외 %d) 차단 %d 예외 요청 %d", seed, passes, exceptedPasses, blocked, len(exceptionIDs))
	if passes == 0 || blocked == 0 {
		t.Fatalf("seed %d: 시나리오가 통과·차단을 모두 만들지 못함", seed)
	}
	if blocked != e.count(`SELECT COUNT(*) FROM audit_logs WHERE action = 'sod.blocked'`) {
		t.Fatalf("seed %d: 차단 기록과 감사 sod.blocked 건수가 다름", seed)
	}
}
