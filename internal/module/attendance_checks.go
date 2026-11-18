package module

import (
	"context"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// attendanceBaseChecks 는 근태·휴가 모듈의 켜기 전 점검표 중 다른 단계의 표(휴가 규칙·근무 유형)에 기대지 않는 항목이다(명세 4.2).
// 휴가 규칙·근무 유형·초기 이관 항목은 그 표를 만드는 단계가 RegisterChecker 로 더한다.
func attendanceBaseChecks(ctx context.Context, q contract.Tx, now time.Time) ([]Check, error) {
	day := contract.DayOf(now)
	year := now.In(contract.KST).Year()
	var out []Check
	// 필수: 올해와 내년의 공휴일 등록(B34). 연차 사용 일수와 근무일 계산이 이 표에 기댄다.
	for _, y := range []int{year, year + 1} {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM holidays WHERE substr(date, 1, 4) = ?`, fmt.Sprintf("%04d", y)).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Check{Label: fmt.Sprintf("%d년 공휴일 등록", y), Required: true, OK: n > 0, Detail: fmt.Sprintf("등록된 공휴일 %d건", n)})
	}
	// 필수: 조직에 등록되지 않은 재직 사용자가 없을 것(입사일은 조직 발령에서 읽는다. 미등록 사용자는 연차가 발생하지 않는다).
	var unreg int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u WHERE u.active = 1 AND NOT EXISTS (
		SELECT 1 FROM org_person_spans s WHERE s.user_id = u.id AND s.left_flag = 0 AND s.valid_from <= ? AND (s.valid_to IS NULL OR s.valid_to > ?))`, string(day), string(day)).Scan(&unreg); err != nil {
		return nil, err
	}
	out = append(out, Check{Label: "모든 재직자의 조직 등록(입사일)", Required: true, OK: unreg == 0, Detail: fmt.Sprintf("조직에 등록되지 않은 사용자 %d명", unreg)})
	// 필수: 현재 법인의 연차 규칙(발생 기준·하루 분량)이 저장되어 있을 것. 규칙이 없으면 연차가 발생하지 않는다(규칙 화면은 모듈이 꺼져 있어도 쓸 수 있다).
	var pol int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_policies WHERE `+contract.SQLEntity(ctx, "leave_policies")+` AND canceled = 0`).Scan(&pol); err != nil {
		return nil, err
	}
	out = append(out, Check{Label: "연차 규칙 저장(발생 기준·하루 분량)", Required: true, OK: pol > 0, Detail: fmt.Sprintf("저장된 규칙 %d건", pol)})
	return out, nil
}
