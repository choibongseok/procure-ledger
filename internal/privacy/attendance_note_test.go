package privacy_test

import (
	"testing"

	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B50: 근태 범주 기록은 SQLite·PG 양쪽에서 같다(PG 는 CHECK 제약 교체 마이그레이션이 적용된 DB).
func TestAttendanceCategoryNoteOnBothDialects(t *testing.T) {
	e := newEnv(t)
	e.note(contactAccess(e.subj1))
	e.note(privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceReason, Fields: []string{privacy.FieldLeaveReason},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: e.subj1}}})
	e.note(privacy.Access{Category: privacy.CatAttendance, Screen: privacy.ScreenAttendanceProof, Fields: []string{privacy.FieldProofFile, privacy.FieldAbsenceReason},
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: e.subj2}}})
	if n := e.count(`SELECT COUNT(*) FROM privacy_access_log WHERE category = 'attendance'`); n != 2 {
		t.Fatalf("근태 범주 기록 %d건", n)
	}
	if res, err := e.rec.Verify(e.ctx); err != nil || !res.OK || res.Checked != 3 {
		t.Fatalf("체인: %+v %v", res, err)
	}
	if err := e.rec.Note(e.ctx, e.actor(), privacy.Access{Category: "health", Screen: privacy.ScreenAttendanceReason,
		Subjects: []privacy.Subject{{Type: privacy.SubjectUser, ID: e.subj1}}}); err == nil {
		t.Fatal("알 수 없는 범주가 통과함")
	}
	// DB 직접 삽입도 열거 밖이면 거부된다.
	if _, err := e.db.Exec(`INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, category, screen, subject_count, subjects_hash, prev_hash, hash) VALUES ('x', ?, 'admin', 'web', 'health', '근태 사유 보기', 0, 'h', 'p', 'qq')`, e.viewer); err == nil {
		t.Fatal("열거 밖 범주가 DB 에 들어감")
	}
}
