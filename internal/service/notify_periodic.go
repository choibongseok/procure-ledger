package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 주기 알림 이벤트 두 가지(B36 단계 3, 명세 3.2 의 payment_due·month_close_check).
//   - 지급 예정(payment_due): 영업일 업무 시작 뒤 첫 실행에서, 오늘부터 N영업일(notify.payment_due_bd, 초기값 3) 안에 만기가 오는 미지급 채무가 있으면
//     지급 권한자(payment:write)에게 하루 한 건. 금액은 알림에 싣지 않는다(앱에서 확인).
//   - 월 마감 점검(month_close_check): 이번 달 notify.close_check_start_bd 영업일째(초기값 3)부터, 지난 달이 마감되지 않았고 마감 전 점검에 차단·경고가 있으면
//     마감 권한자(inventory:close)에게 notify.close_check_every_days(초기값 7)일 간격으로 한 건.
//
// 휴일·주말에는 새로 만들지 않는다. 다음 영업일의 첫 실행이 한 건으로 만든다(B34 영업일 달력, 같은 isBusinessDay). 앱 안 알림을 만들고(Base.notify),
// 메일·웹훅은 같은 흐름(Deliver 훅)으로 나간다. 서버 프로세스만 돈다(erp-mcp 는 돌지 않는다).

// BusinessDayFunc 는 B34 영업일 판정(isBusinessDay)을 알림 채널 쪽에 꽂는 함수다(영업일의 정의가 한 곳에만 있게).
// 질의는 받은 q 로 한다: 업무 쓰기 트랜잭션 안에서 불리는 경로(알림 큐의 업무 시간 판정)가 b.DB 를 따로 읽으면 SQLite 단일 연결에서 교착이 난다.
func BusinessDayFunc(b Base) func(ctx context.Context, q contract.Tx, day string) (bool, error) {
	return func(ctx context.Context, q contract.Tx, day string) (bool, error) {
		return b.isBusinessDay(ctx, q, day)
	}
}

// PeriodicNotifier 는 위 두 주기 알림을 만든다.
type PeriodicNotifier struct {
	Base
	Close *MonthCloseService
}

// NewPeriodicNotifier 는 주기 알림 생성기를 만든다.
func NewPeriodicNotifier(b Base, closeSvc *MonthCloseService) *PeriodicNotifier {
	return &PeriodicNotifier{Base: b, Close: closeSvc}
}

func (n *PeriodicNotifier) setting(ctx context.Context, key string, def int) int {
	var v string
	if err := n.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	x, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || x <= 0 {
		return def
	}
	return x
}

// workStartMinutes 는 업무 시작 시각(KST, 분)이다. 설정이 없거나 잘못되면 09:00.
func (n *PeriodicNotifier) workStartMinutes(ctx context.Context) int {
	var v string
	if err := n.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'notify.work_start'`).Scan(&v); err != nil {
		return 9 * 60
	}
	p := strings.Split(strings.TrimSpace(v), ":")
	if len(p) != 2 {
		return 9 * 60
	}
	h, e1 := strconv.Atoi(p[0])
	m, e2 := strconv.Atoi(p[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 9 * 60
	}
	return h*60 + m
}

// Tick 은 한 번 돈다: 영업일이고 업무 시작 시각 이후일 때만 두 알림을 만든다. 같은 날 여러 번 불러도 알림은 한 번만 만들어진다.
func (n *PeriodicNotifier) Tick(ctx context.Context) error {
	now := n.now().In(contract.KST)
	today := now.Format("2006-01-02")
	if ok, err := n.isBusinessDay(ctx, n.DB, today); err != nil || !ok {
		return err
	}
	if now.Hour()*60+now.Minute() < n.workStartMinutes(ctx) {
		return nil
	}
	if err := n.paymentDue(ctx, today); err != nil {
		return fmt.Errorf("지급 예정 알림: %w", err)
	}
	if err := n.monthCloseCheck(ctx, today); err != nil {
		return fmt.Errorf("월 마감 점검 알림: %w", err)
	}
	return nil
}

// addBusinessDays 는 day 로부터 영업일 count 개 뒤의 날짜다(day 자신은 세지 않는다). 공휴일을 끝없이 등록해도 멈추도록 400일까지만 본다.
func (n *PeriodicNotifier) addBusinessDays(ctx context.Context, day string, count int) (string, error) {
	d := parseDay(day)
	left := count
	for i := 1; i <= 400 && left > 0; i++ {
		s := d.AddDate(0, 0, i).Format("2006-01-02")
		ok, err := n.isBusinessDay(ctx, n.DB, s)
		if err != nil {
			return "", err
		}
		if ok {
			left--
			if left == 0 {
				return s, nil
			}
		}
	}
	return day, nil
}

// businessDayIndex 는 day 가 그 달의 몇째 영업일인지다(1일부터 day 까지의 영업일 수, day 포함).
func (n *PeriodicNotifier) businessDayIndex(ctx context.Context, day string) (int, error) {
	t := parseDay(day)
	cnt := 0
	for d := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC); !d.After(t); d = d.AddDate(0, 0, 1) {
		ok, err := n.isBusinessDay(ctx, n.DB, d.Format("2006-01-02"))
		if err != nil {
			return 0, err
		}
		if ok {
			cnt++
		}
	}
	return cnt, nil
}

func ymdInt(day string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(day, "-", ""), 10, 64)
	return n
}

func (n *PeriodicNotifier) paymentDue(ctx context.Context, today string) error {
	to, err := n.addBusinessDays(ctx, today, n.setting(ctx, "notify.payment_due_bd", 3))
	if err != nil {
		return err
	}
	due, err := repository.SQLPayments{}.DuePayables(ctx, n.DB, today, to)
	if err != nil || len(due) == 0 {
		return err
	}
	return n.Txs.WithTx(ctx, func(tx contract.Tx) error {
		users, err := repository.SQLLines{}.ActiveByPerm(ctx, tx, contract.PermPaymentWrite)
		if err != nil {
			return err
		}
		ref := ymdInt(today)
		for _, u := range users {
			var done int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'payment_due' AND ref_type = 'payment_due' AND ref_id = ?`, u.ID, ref).Scan(&done); err != nil {
				return err
			}
			if done > 0 {
				continue
			}
			if err := n.notify(ctx, tx, repository.NotificationInsert{UserID: u.ID, Kind: "payment_due", Title: "지급 예정 건이 있습니다",
				Body: fmt.Sprintf("%s까지 만기가 돌아오는 미지급 채무가 %d건 있습니다. 지급 일정에서 확인하세요.", dayLabel(to), len(due)),
				Link: "/finance/payments", RefType: "payment_due", RefID: ref}); err != nil {
				return err
			}
		}
		return nil
	})
}

func dayLabel(day string) string {
	t := parseDay(day)
	return fmt.Sprintf("%d월 %d일", int(t.Month()), t.Day())
}

func (n *PeriodicNotifier) monthCloseCheck(ctx context.Context, today string) error {
	idx, err := n.businessDayIndex(ctx, today)
	if err != nil || idx < n.setting(ctx, "notify.close_check_start_bd", 3) {
		return err
	}
	month := addMonths(parseDay(today).Format("2006-01"), -1)
	rec, ok, err := repository.SQLMonthClose{}.Get(ctx, n.DB, month)
	if err != nil || (ok && rec.Status == "closed") {
		return err
	}
	first, err := repository.SQLMonthClose{}.EarliestLedgerMonth(ctx, n.DB)
	if err != nil || first == "" || month < first {
		return err // 재고 기록이 아직 없는 달은 마감 대상이 아니다
	}
	pc, err := n.Close.precheck(ctx, n.DB, month)
	if err != nil {
		return err
	}
	if len(pc.Blockers)+len(pc.Warnings) == 0 {
		return nil
	}
	every := n.setting(ctx, "notify.close_check_every_days", 7)
	return n.Txs.WithTx(ctx, func(tx contract.Tx) error {
		users, err := repository.SQLLines{}.ActiveByPerm(ctx, tx, contract.PermInventoryClose)
		if err != nil {
			return err
		}
		for _, u := range users {
			var last string
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at), '') FROM notifications WHERE user_id = ? AND kind = 'month_close_check'`, u.ID).Scan(&last)
			if last != "" {
				lt, perr := time.Parse("2006-01-02T15:04:05Z", last)
				if perr == nil && parseDay(lt.In(contract.KST).Format("2006-01-02")).AddDate(0, 0, every).After(parseDay(today)) {
					continue // 같은 사람에게는 주기(초기값 7일) 안에 다시 만들지 않는다
				}
			}
			if err := n.notify(ctx, tx, repository.NotificationInsert{UserID: u.ID, Kind: "month_close_check", Title: "월 마감 전 점검 항목이 있습니다",
				Body: fmt.Sprintf("%s 마감 전 점검에 차단 %d건, 경고 %d건이 있습니다. 월 마감 화면에서 확인하세요.", monthLabel(month), len(pc.Blockers), len(pc.Warnings)),
				Link: "/inventory/close", RefType: "month_close", RefID: ymdInt(month)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Run 은 서버가 켜져 있는 동안 10분마다 Tick 을 부른다.
func (n *PeriodicNotifier) Run(ctx context.Context) {
	tick := func() {
		if err := n.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("notify: 주기 알림 생성 실패: %v", err)
		}
	}
	tick()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
