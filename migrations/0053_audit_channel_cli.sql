-- 0053: 감사 기록 경로에 'cli' 추가(B38 D7, 3.4·11절).
-- 서버 호스트의 명령줄 도구(erp-admin, 서버 하위 명령)가 남기는 기록에 맞는 경로 값이 없어서 지금까지 'web' 으로 거짓 기록했다.
-- audit_logs 의 CHECK 는 바꿀 수 없어 표를 새로 만들어 옮긴다. 행 내용(채널 문자열 포함)은 그대로 복사하므로 해시 체인은 영향이 없고
-- seq 도 그대로다(AUTOINCREMENT 시퀀스는 복사한 최대 seq 로 이어진다). DROP TABLE 은 삭제 트리거를 실행하지 않는다.
-- 추가 전용 트리거와 색인(0001, 0008)은 새 표에 다시 만든다.
CREATE TABLE audit_logs_new (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    actor_id    INTEGER NOT NULL,                     -- 사용자 없는 사건은 0 (FK 없음: 기록은 사용자 삭제와 무관)
    actor_role  TEXT NOT NULL,
    channel     TEXT NOT NULL CHECK (channel IN ('web','mcp','llmgw','cli')),
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id   TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('success','denied','error')),
    diff        TEXT NOT NULL DEFAULT '[]',           -- JSON [{key,old,new}]
    detail      TEXT NOT NULL DEFAULT '',
    prev_hash   TEXT NOT NULL,                        -- 첫 레코드는 빈 문자열
    hash        TEXT NOT NULL UNIQUE
);
INSERT INTO audit_logs_new (seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash)
    SELECT seq, at, actor_id, actor_role, channel, action, target_type, target_id, result, diff, detail, prev_hash, hash FROM audit_logs ORDER BY seq;
DROP TABLE audit_logs;
ALTER TABLE audit_logs_new RENAME TO audit_logs;
CREATE INDEX idx_audit_target ON audit_logs(target_type, target_id);
CREATE INDEX idx_audit_actor  ON audit_logs(actor_id);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE INDEX idx_audit_channel_at ON audit_logs(channel, at);
CREATE TRIGGER trg_audit_no_update BEFORE UPDATE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;
CREATE TRIGGER trg_audit_no_delete BEFORE DELETE ON audit_logs
BEGIN SELECT RAISE(ABORT, 'audit_logs 는 추가 전용입니다'); END;
