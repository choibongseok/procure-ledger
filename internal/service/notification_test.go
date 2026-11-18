package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func (e *env) addNote(t *testing.T, userID int64, title string) int64 {
	t.Helper()
	id, err := dbutil.InsertID(context.Background(), e.db, `INSERT INTO notifications (user_id, kind, title, body, link, created_at) VALUES (?, 'approval_reassigned', ?, '본문', '/approvals/1', ?)`,
		userID, title, e.now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// 내 알림 목록·안 읽은 건수는 본인 것만 돌려준다(AC-142).
func TestNotificationsAreOwnOnly(t *testing.T) {
	e := newEnv(t)
	mine := e.addNote(t, uStaff, "내 알림 1")
	e.addNote(t, uStaff, "내 알림 2")
	other := e.addNote(t, uLead, "남의 알림")

	list, err := e.notification.List(as(uStaff), domain.NotificationFilter{})
	if err != nil || len(list) != 2 || list[0].Title != "내 알림 2" {
		t.Fatalf("목록 %+v %v", list, err)
	}
	if n, _ := e.notification.UnreadCount(as(uStaff)); n != 2 {
		t.Fatalf("안 읽은 건수 %d", n)
	}
	// 남의 알림·없는 번호 읽음 처리는 404 로 존재를 숨긴다(E39).
	is(t, e.notification.MarkRead(as(uStaff), other), contract.ErrNotFound)
	is(t, e.notification.MarkRead(as(uStaff), 9999), contract.ErrNotFound)
	if n, _ := e.notification.UnreadCount(as(uLead)); n != 1 {
		t.Fatalf("남의 알림이 읽음 처리됨: %d", n)
	}
	// 한 건 읽음, 다시 읽어도 성공, 읽은 알림은 '안 읽음만' 필터에서 빠진다.
	if err := e.notification.MarkRead(as(uStaff), mine); err != nil {
		t.Fatal(err)
	}
	if err := e.notification.MarkRead(as(uStaff), mine); err != nil {
		t.Fatalf("이미 읽은 알림 재처리는 성공이어야 한다: %v", err)
	}
	unread, _ := e.notification.List(as(uStaff), domain.NotificationFilter{UnreadOnly: true})
	if len(unread) != 1 || unread[0].Title != "내 알림 2" {
		t.Fatalf("안 읽음만 %+v", unread)
	}
	all, _ := e.notification.List(as(uStaff), domain.NotificationFilter{})
	if len(all) != 2 || all[1].ReadAt == nil || all[0].ReadAt != nil {
		t.Fatalf("읽은 시각 %+v", all)
	}
	// 모두 읽음은 본인 것만, 건수를 돌려준다.
	e.addNote(t, uStaff, "내 알림 3")
	n, err := e.notification.MarkAllRead(as(uStaff))
	if err != nil || n != 2 {
		t.Fatalf("모두 읽음 %d %v", n, err)
	}
	if c, _ := e.notification.UnreadCount(as(uLead)); c != 1 {
		t.Fatalf("남의 알림이 읽음 처리됨: %d", c)
	}
}

// 읽음 처리는 read_at 만 바꾸고 삭제 경로가 없다(AC-143). 목록 한도는 100 이다.
func TestNotificationsOnlyReadAtChangesAndLimit(t *testing.T) {
	e := newEnv(t)
	id := e.addNote(t, uStaff, "알림")
	if _, err := e.db.Exec(`UPDATE notifications SET body = '바꿈' WHERE id = ?`, id); err == nil {
		t.Fatal("읽음 처리 외의 변경은 DB 가 거부해야 한다")
	}
	if _, err := e.db.Exec(`DELETE FROM notifications WHERE id = ?`, id); err == nil {
		t.Fatal("알림 삭제는 거부되어야 한다")
	}
	for i := 0; i < 120; i++ {
		e.addNote(t, uStaff, "많은 알림")
	}
	list, err := e.notification.List(as(uStaff), domain.NotificationFilter{Limit: 500})
	if err != nil || len(list) != 100 {
		t.Fatalf("한도 100: %d %v", len(list), err)
	}
	older, _ := e.notification.List(as(uStaff), domain.NotificationFilter{Limit: 5, BeforeID: list[len(list)-1].ID})
	if len(older) != 5 || older[0].ID >= list[len(list)-1].ID {
		t.Fatalf("커서 %+v", older)
	}
}

// notification:read 는 다섯 역할 모두 갖고, 로그인하지 않으면 거부된다(AC-144).
func TestNotificationPermissionIsForAllRoles(t *testing.T) {
	e := newEnv(t)
	for _, uid := range []int64{uStaff, uLead, uFinance, uAuditor, uAdmin} {
		if _, err := e.notification.List(as(uid), domain.NotificationFilter{}); err != nil {
			t.Fatalf("사용자 %d: %v", uid, err)
		}
	}
	if _, err := e.notification.List(contractNoUser(), domain.NotificationFilter{}); err == nil {
		t.Fatal("로그인하지 않은 호출은 거부되어야 한다")
	}
}
