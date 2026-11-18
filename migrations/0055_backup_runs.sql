-- 0055: 백업·복구 실행 기록(B38 2단계, 6.6·11절).
-- erp-admin 이 라이브 DB 에 기록한다(서버가 켜져 있으면 백업 상태 화면이 읽는다). 스냅샷 뒤에 기록하므로 해당 백업에는 자기 기록이 없다(정상).
-- 추가 전용이다: 수정·삭제는 트리거가 막는다. 비밀·경로·호스트 이름은 담지 않는다(세트 이름만).
CREATE TABLE backup_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL CHECK (kind IN ('backup','restore','rehearsal')),
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('success','failed')),
    set_name    TEXT NOT NULL DEFAULT '',
    db_kind     TEXT NOT NULL DEFAULT '',
    bytes       INTEGER NOT NULL DEFAULT 0,
    chain_seq   INTEGER NOT NULL DEFAULT 0,
    error_code  TEXT NOT NULL DEFAULT '',
    tool_version TEXT NOT NULL DEFAULT '',
    warnings    TEXT NOT NULL DEFAULT ''             -- 경고 항목(업무 불변식 위반의 항목·건수 등). 실행을 막지 않은 문제
);
CREATE INDEX idx_backup_runs_kind ON backup_runs(kind, id);
CREATE TRIGGER trg_backup_runs_no_update BEFORE UPDATE ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs 는 추가 전용입니다'); END;
CREATE TRIGGER trg_backup_runs_no_delete BEFORE DELETE ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs 는 추가 전용입니다'); END;
