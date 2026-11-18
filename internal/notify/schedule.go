package notify

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 업무 시간 외 묶음 발송(B36 단계 3, 명세 4.4).
// 업무 시간 밖(밤·주말·휴일)에 생긴 메일 알림은 즉시 보내지 않고 held(보류)로 두었다가, 다음 영업일 업무 시작 때 그 사람의 보류 건을
// 종류별 건수만 담은 메일 한 통으로 보낸다. 독촉(approval_nudge)은 묶지 않는다(D9). 보류가 상한(기본 72시간)을 넘기면 업무 시간이 아니어도 푼다.
// 묶음은 메일에만 적용한다(웹훅은 채널 방이라 즉시).

// BusinessDayFunc 는 날짜("YYYY-MM-DD", KST)가 영업일인지 알려 주는 함수다. 영업일 판정은 B34 의 한 정의(service.isBusinessDay)를 그대로 꽂는다.
// q 는 호출한 쪽이 이미 쓰고 있는 질의 수단(업무 쓰기 트랜잭션 안에서는 그 트랜잭션)이다. SQLite 는 연결이 하나라 트랜잭션 안에서 다른 연결(DB 풀)로 읽으면
// 영원히 기다리고(교착), PG 에서도 커밋 전 변경을 못 보는 별도 읽기가 되므로 판정은 반드시 받은 q 로 읽는다.
type BusinessDayFunc func(ctx context.Context, q contract.Tx, day string) (bool, error)

// SetBusinessDay 는 영업일 판정을 꽂는다(조립 단계). 꽂지 않으면 모든 날을 영업일로 본다(휴일·주말 구분 없음).
func (s *Service) SetBusinessDay(f BusinessDayFunc) { s.o.BusinessDay = f }

func (s *Service) businessDay(ctx context.Context, q contract.Tx, day string) bool {
	if s.o.BusinessDay == nil {
		return true
	}
	ok, err := s.o.BusinessDay(ctx, q, day)
	if err != nil {
		return true // 판정에 실패하면 보류하지 않는다(알림이 묻히는 것보다 일찍 가는 쪽이 안전하다)
	}
	return ok
}

func strSetting(ctx context.Context, q querier, key, def string) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

// ParseClock 은 "HH:MM" 을 분(0~1439)으로 바꾼다.
func ParseClock(s string) (int, bool) {
	p := strings.Split(strings.TrimSpace(s), ":")
	if len(p) != 2 || len(p[0]) != 2 || len(p[1]) != 2 {
		return 0, false
	}
	h, e1 := strconv.Atoi(p[0])
	m, e2 := strconv.Atoi(p[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// workWindow 는 업무 시간 시작·끝(분)이다. 설정이 잘못되면 09:00~18:00 이다.
func workWindow(ctx context.Context, q querier) (start, end int) {
	start, ok1 := ParseClock(strSetting(ctx, q, "notify.work_start", "09:00"))
	end, ok2 := ParseClock(strSetting(ctx, q, "notify.work_end", "18:00"))
	if !ok1 || !ok2 || end <= start {
		return 9 * 60, 18 * 60
	}
	return start, end
}

// InWorkHours 는 at 이 업무 시간 안(영업일의 시작 이상·끝 미만, KST)인지다.
func (s *Service) InWorkHours(ctx context.Context, q contract.Tx, at time.Time) bool {
	k := at.In(kst)
	if !s.businessDay(ctx, q, k.Format("2006-01-02")) {
		return false
	}
	start, end := workWindow(ctx, q)
	m := k.Hour()*60 + k.Minute()
	return m >= start && m < end
}

// digestOn 은 그 사람이 업무 시간 외 묶음을 받는지다: 본인 선택, 없으면 관리자 기본값.
func digestOn(ctx context.Context, q querier, userID int64) bool {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT digest FROM notification_user_opts WHERE user_id = ?`, userID).Scan(&v); err == nil {
		return v == "on"
	}
	return strSetting(ctx, q, "notify.digest_default", "0") == "1"
}

// releaseHeld 는 보류 건을 푼다: 업무 시간이 되었거나 가장 오래된 보류 건이 상한(notify.hold_max_hours, 기본 72)을 넘긴 사람의 건을
// 모두 queued 로 돌리고 같은 묶음 번호(그 사람 보류 건 중 가장 작은 번호)를 붙인다. 발송 때 묶음 번호가 같은 건은 요약 메일 한 통이 된다.
func (s *Service) releaseHeld(ctx context.Context, now time.Time) error {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT user_id, MIN(id), MIN(created_at) FROM notification_deliveries WHERE status = 'held' GROUP BY user_id`)
	if err != nil {
		return err
	}
	type heldUser struct {
		user, minID int64
		oldest      string
	}
	var us []heldUser
	for rows.Next() {
		var h heldUser
		if err := rows.Scan(&h.user, &h.minID, &h.oldest); err != nil {
			rows.Close()
			return err
		}
		us = append(us, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(us) == 0 {
		return nil
	}
	inHours := s.InWorkHours(ctx, s.o.DB, now)
	maxAge := time.Duration(intSetting(ctx, s.o.DB, "notify.hold_max_hours", 72)) * time.Hour
	for _, h := range us {
		oldest, perr := time.Parse(tsLayout, h.oldest)
		if !inHours && !(perr == nil && now.Sub(oldest) >= maxAge) {
			continue
		}
		if _, err := s.o.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', digest_id = ?, next_try_at = ? WHERE user_id = ? AND status = 'held'`, h.minID, ts(now), h.user); err != nil {
			return err
		}
	}
	return nil
}

// ComposeDigest 는 업무 시간 외 묶음 요약 메일을 틀로 만든다: 종류별 건수만 담고(최대 20줄, 나머지는 "외 N종") 개별 항목은 나열하지 않는다.
func ComposeDigest(counts map[string]int, baseURL string) (Message, error) {
	link, err := LinkURL(baseURL, "/notifications")
	if err != nil {
		return Message{}, err
	}
	var b strings.Builder
	b.WriteString("업무 시간 밖에 쌓인 알림입니다.\n")
	lines, rest := 0, 0
	for _, k := range Kinds {
		n := counts[k.Kind]
		if n == 0 {
			continue
		}
		if lines >= 20 {
			rest++
			continue
		}
		fmt.Fprintf(&b, "- %s %d건\n", k.Name, n)
		lines++
	}
	if rest > 0 {
		fmt.Fprintf(&b, "외 %d종\n", rest)
	}
	b.WriteString("열기: " + link + "\n")
	settings, err := LinkURL(baseURL, SettingsLinkPath) // 수신 거부 안내(명세 4.2): 일반 메일과 같다
	if err != nil {
		return Message{}, err
	}
	b.WriteString("\n알림 설정: " + settings + "\n")
	if b.Len() > maxBody {
		return Message{}, fmt.Errorf("메시지가 너무 깁니다")
	}
	return Message{Subject: subjectPrefix + "알림 요약", Body: b.String()}, nil
}
