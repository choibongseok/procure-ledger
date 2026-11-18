-- 0102: 출퇴근·근무 유형·정정·근태 마감·주 근로시간 경고(B50 12.3,). 모두 법인 소유 표다(entity_id, 기본 법인 1). 날짜는 KST YYYY-MM-DD, 시각은 RFC3339 UTC.
--  - work_types: 근무 유형(고정 fixed·시차 staggered·선택 flexible). 지우지 않고 사용 중지만 한다. 배정이 없는 사람은 서비스의 기본 유형(고정 09:00~18:00, 휴게 12:00~13:00)으로 본다.
--  - work_assignments: 사람별 근무 유형 배정 이력(효력일, 추가 전용). 같은 사람·같은 효력일은 하나다.
--  - attendance_records: 하루 한 행(사용자·근무일 유일). 체크인·체크아웃은 서버 시각. 고치는 길은 정정 줄(attendance_corrections)을 거친 서비스뿐이고 행은 지울 수 없다.
--  - attendance_corrections: 정정 요청·결과 줄. 요청 때 이전 값과 새 값·사유가 정해지고 이후에는 상태(requested → applied·rejected)와 결정자만 바뀐다.
--  - attendance_closes: 법인×월 근태 마감. 해제하면 해제 정보가 남고 다시 마감하면 새로 쓴다(이력은 감사에 있다).
--  - attendance_warnings: 주 근로시간 경고 알림을 보낸 기록(사용자·주 시작일·단계 유일 = 같은 주 같은 단계는 한 번만 알린다).
CREATE TABLE work_types (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id     INTEGER NOT NULL DEFAULT 1,
    code          TEXT NOT NULL CHECK (code GLOB '[a-z]*' AND length(code) BETWEEN 2 AND 30),
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 30),
    kind          TEXT NOT NULL CHECK (kind IN ('fixed','staggered','flexible')),
    start_time    TEXT NOT NULL DEFAULT '' CHECK (start_time = '' OR start_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    end_time      TEXT NOT NULL DEFAULT '' CHECK (end_time = '' OR end_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    break_start   TEXT NOT NULL DEFAULT '' CHECK (break_start = '' OR break_start GLOB '[0-2][0-9]:[0-5][0-9]'),
    break_end     TEXT NOT NULL DEFAULT '' CHECK (break_end = '' OR break_end GLOB '[0-2][0-9]:[0-5][0-9]'),
    core_start    TEXT NOT NULL DEFAULT '' CHECK (core_start = '' OR core_start GLOB '[0-2][0-9]:[0-5][0-9]'),
    core_end      TEXT NOT NULL DEFAULT '' CHECK (core_end = '' OR core_end GLOB '[0-2][0-9]:[0-5][0-9]'),
    weekly_minutes INTEGER NOT NULL DEFAULT 2400 CHECK (weekly_minutes BETWEEN 600 AND 3120),
    active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at    TEXT NOT NULL,
    UNIQUE (entity_id, code)
);
CREATE TRIGGER trg_work_types_no_delete BEFORE DELETE ON work_types BEGIN SELECT RAISE(ABORT, '근무 유형은 지울 수 없습니다(사용 중지만 됩니다)'); END;

CREATE TABLE work_assignments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id    INTEGER NOT NULL DEFAULT 1,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    work_type_id INTEGER NOT NULL REFERENCES work_types(id),
    valid_from   TEXT NOT NULL CHECK (valid_from GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    created_by   INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    UNIQUE (user_id, valid_from)
);
CREATE INDEX idx_work_assignments_user ON work_assignments(user_id, valid_from);
CREATE TRIGGER trg_work_assignments_no_update BEFORE UPDATE ON work_assignments BEGIN SELECT RAISE(ABORT, '근무 유형 배정 이력은 고칠 수 없습니다(추가 전용)'); END;
CREATE TRIGGER trg_work_assignments_no_delete BEFORE DELETE ON work_assignments BEGIN SELECT RAISE(ABORT, '근무 유형 배정 이력은 지울 수 없습니다(추가 전용)'); END;

CREATE TABLE attendance_records (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id    INTEGER NOT NULL DEFAULT 1,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    work_date    TEXT NOT NULL CHECK (work_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    check_in_at  TEXT NOT NULL DEFAULT '',
    check_out_at TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL CHECK (source IN ('web','admin','correction')),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (user_id, work_date)
);
CREATE INDEX idx_attendance_records_date ON attendance_records(entity_id, work_date);
CREATE TRIGGER trg_attendance_records_no_delete BEFORE DELETE ON attendance_records BEGIN SELECT RAISE(ABORT, '출퇴근 기록은 지울 수 없습니다(정정 줄로만 고칩니다)'); END;
CREATE TRIGGER trg_attendance_records_fixed BEFORE UPDATE ON attendance_records
WHEN NEW.user_id IS NOT OLD.user_id OR NEW.work_date IS NOT OLD.work_date OR NEW.entity_id IS NOT OLD.entity_id
BEGIN SELECT RAISE(ABORT, '출퇴근 기록의 사용자·근무일은 고칠 수 없습니다'); END;

CREATE TABLE attendance_corrections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id    INTEGER NOT NULL DEFAULT 1,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    work_date    TEXT NOT NULL CHECK (work_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    old_in       TEXT NOT NULL DEFAULT '',
    old_out      TEXT NOT NULL DEFAULT '',
    new_in       TEXT NOT NULL DEFAULT '',
    new_out      TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
    requested_by INTEGER NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('requested','applied','rejected')),
    approved_by  INTEGER NOT NULL DEFAULT 0,
    decide_note  TEXT NOT NULL DEFAULT '' CHECK (length(decide_note) <= 200),
    created_at   TEXT NOT NULL,
    decided_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_attendance_corrections_user ON attendance_corrections(entity_id, user_id, work_date);
CREATE INDEX idx_attendance_corrections_status ON attendance_corrections(entity_id, status);
CREATE TRIGGER trg_attendance_corrections_no_delete BEFORE DELETE ON attendance_corrections BEGIN SELECT RAISE(ABORT, '정정 줄은 지울 수 없습니다(추가 전용)'); END;
CREATE TRIGGER trg_attendance_corrections_fixed BEFORE UPDATE ON attendance_corrections
WHEN NEW.user_id IS NOT OLD.user_id OR NEW.work_date IS NOT OLD.work_date OR NEW.old_in IS NOT OLD.old_in OR NEW.old_out IS NOT OLD.old_out OR NEW.new_in IS NOT OLD.new_in
  OR NEW.new_out IS NOT OLD.new_out OR NEW.reason IS NOT OLD.reason OR NEW.requested_by IS NOT OLD.requested_by OR NEW.entity_id IS NOT OLD.entity_id OR NEW.created_at IS NOT OLD.created_at
  OR OLD.status <> 'requested'
BEGIN SELECT RAISE(ABORT, '정정 줄은 고칠 수 없습니다(요청 중인 줄의 결정만 기록됩니다)'); END;

CREATE TABLE attendance_closes (
    entity_id     INTEGER NOT NULL DEFAULT 1,
    month         TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    closed_by     INTEGER NOT NULL,
    closed_at     TEXT NOT NULL,
    reopened_by   INTEGER NOT NULL DEFAULT 0,
    reopened_at   TEXT NOT NULL DEFAULT '',
    reopen_reason TEXT NOT NULL DEFAULT '' CHECK (length(reopen_reason) <= 200),
    PRIMARY KEY (entity_id, month)
);
CREATE TRIGGER trg_attendance_closes_no_delete BEFORE DELETE ON attendance_closes BEGIN SELECT RAISE(ABORT, '근태 마감 기록은 지울 수 없습니다'); END;

CREATE TABLE attendance_warnings (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    week_start TEXT NOT NULL CHECK (week_start GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    level      TEXT NOT NULL CHECK (level IN ('caution','over')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, week_start, level)
);
