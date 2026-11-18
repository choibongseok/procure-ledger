package service_test

import (
	"fmt"
	"strings"
	"testing"
)

// B50 AC-5035·6.2 의 3: 휴직 기간에는 휴가를 신청할 수 없다(문구에 구간이 보인다). 휴직 전후 날짜는 신청된다.
func TestLeaveRequestBlockedDuringAbsence(t *testing.T) {
	e := newEnv(t)
	l := e.leave(t)
	l.policy(t)
	l.open(t, uStaff, 5)
	// 조직 서비스가 쓴 것과 같은 모양의 구간(11. 17.~11. 19. 휴직)을 넣는다. 구간 표는 조직 서비스만 쓰므로 쓰기 보호 행을 잠깐 연다.
	for _, q := range []string{
		`INSERT INTO org_units (id, code) VALUES (7001, 'ABS') ON CONFLICT DO NOTHING`,
		`INSERT INTO org_write_guard (id) VALUES (1)`,
		`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id, absence_kind) VALUES (` + fmt.Sprint(uStaff) + `, '2026-01-01', '2026-11-17', 0, 1, 7001, '')`,
		`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id, absence_kind) VALUES (` + fmt.Sprint(uStaff) + `, '2026-11-17', '2026-11-20', 0, 1, 7001, 'illness')`,
		`INSERT INTO org_person_spans (user_id, valid_from, valid_to, left_flag, rank_id, primary_unit_id, absence_kind) VALUES (` + fmt.Sprint(uStaff) + `, '2026-11-20', NULL, 0, 1, 7001, '')`,
		`DELETE FROM org_write_guard`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-18"))
	wantValidation(t, err, "휴직 기간(11. 17.~11. 19.)에는 신청할 수 없습니다")
	if _, err := l.svc.Preview(as(uStaff), in("annual", "2026-11-19", "2026-11-19")); err == nil {
		t.Fatal("휴직 마지막 날도 신청할 수 없다")
	}
	// 휴직 전날과 복직일은 신청된다.
	if _, err := l.svc.Request(as(uStaff), in("annual", "2026-11-16", "2026-11-16")); err != nil {
		t.Fatalf("휴직 전날: %v", err)
	}
	if _, err := l.svc.Request(as(uStaff), in("annual", "2026-11-20", "2026-11-20")); err != nil {
		t.Fatalf("복직일: %v", err)
	}
	// 구분(질병)은 오류 문구에 없다.
	_, err = l.svc.Request(as(uStaff), in("annual", "2026-11-17", "2026-11-17"))
	if err != nil && (strings.Contains(err.Error(), "질병") || strings.Contains(err.Error(), "illness")) {
		t.Fatalf("휴직 구분이 문구에 새면 안 된다: %v", err)
	}
}
