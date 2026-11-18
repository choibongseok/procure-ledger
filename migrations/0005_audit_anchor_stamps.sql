-- 0005: 외부 앵커 기록 대장(B9).
-- 앵커 파일에 줄을 쓸 때마다 한 행을 남겨, 파일이 사라지거나 줄이 줄어도 DB 쪽 근거로 유실을 알아본다.
-- kind='stamp' 는 파일에 쓴 줄(line_hash 는 줄 원문의 SHA-256), kind='reset' 은 유실 뒤 명시적 재시작이다.
-- 재시작 행 이후의 stamp 만 유실 검사 대상이고, reset 행이 하나라도 있으면 "앵커 유실 이력 있음"이 영구 표시된다.
CREATE TABLE audit_anchor_stamps (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    kind      TEXT NOT NULL CHECK (kind IN ('stamp','reset')),
    seq       INTEGER NOT NULL DEFAULT 0,
    line_hash TEXT NOT NULL DEFAULT '',
    at        TEXT NOT NULL
);
CREATE TRIGGER trg_anchor_stamps_no_update BEFORE UPDATE ON audit_anchor_stamps
BEGIN SELECT RAISE(ABORT, 'audit_anchor_stamps 는 추가 전용입니다'); END;
CREATE TRIGGER trg_anchor_stamps_no_delete BEFORE DELETE ON audit_anchor_stamps
BEGIN SELECT RAISE(ABORT, 'audit_anchor_stamps 는 추가 전용입니다'); END;
