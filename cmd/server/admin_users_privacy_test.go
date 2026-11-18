package main

import (
	"encoding/json"
	"testing"
)

// B41: 다른 사람의 이메일 원문이 실리는 관리 목록(사용자 관리·로그인 설정 사용자 목록)은 열람 기록을 남긴다.
// 본인 한 명만 보이는 경우는 남기지 않고, 목록을 보는 사람 본인은 정보주체에서 제외한다.
func TestAdminUserListsAreRecordedInPrivacyLog(t *testing.T) {
	e := newPlatformEnv(t, true)
	admin := e.browser()
	e.login(admin, "admin@pl.test")
	count := func(screen string) (rows, subjects int) {
		t.Helper()
		if err := e.d.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(subject_count), 0) FROM privacy_access_log WHERE screen = ? AND category = 'contact' AND fields = '["email"]'`, screen).Scan(&rows, &subjects); err != nil {
			t.Fatal(err)
		}
		return
	}
	code, body := e.call(admin, "GET", "/api/admin/users", "", nil)
	if code != 200 {
		t.Fatalf("사용자 목록 %d %s", code, body)
	}
	var users []struct{ ID int64 }
	if err := json.Unmarshal([]byte(body), &users); err != nil || len(users) < 2 {
		t.Fatalf("목록: %v %s", err, body)
	}
	rows, subjects := count("사용자 관리 화면")
	if rows != 1 || subjects != len(users)-1 {
		t.Fatalf("사용자 관리 화면 기록 %d건·정보주체 %d명(기대 1건·%d명: 본인 제외)", rows, subjects, len(users)-1)
	}
	code, body = e.call(admin, "GET", "/api/admin/auth/users", "", nil)
	if code != 200 {
		t.Fatalf("로그인 설정 사용자 목록 %d %s", code, body)
	}
	var auth struct{ Users []struct{ UserID int64 } }
	if err := json.Unmarshal([]byte(body), &auth); err != nil || len(auth.Users) < 2 {
		t.Fatalf("목록: %v %s", err, body)
	}
	rows, subjects = count("로그인 설정 사용자 목록")
	if rows != 1 || subjects != len(auth.Users)-1 {
		t.Fatalf("로그인 설정 사용자 목록 기록 %d건·정보주체 %d명(기대 1건·%d명)", rows, subjects, len(auth.Users)-1)
	}
	// 기록에는 이메일 원문이 없다.
	var leaked int
	if err := e.d.DB.QueryRow(`SELECT COUNT(*) FROM privacy_access_log WHERE fields LIKE '%@%' OR screen LIKE '%@%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("기록에 원문: %d %v", leaked, err)
	}
}
