-- 0063: 알림 웹훅 대상 종류에 'termspace' 추가(B36 후속).
-- termspace 는 슬랙처럼 수신 웹훅(https://<호스트>/hooks/tsh_<토큰>)을 받는 채팅·협업툴이다. 방에는 건수+링크만 보낸다(B36 원칙 그대로).
-- notification_targets.type 과 notification_deliveries.channel 의 CHECK 는 바꿀 수 없어 두 표를 새로 만들어 옮긴다.
-- 행 내용·번호(id)는 그대로 복사하고 AUTOINCREMENT 시퀀스도 이어 붙인다. DROP TABLE 은 삭제 트리거를 실행하지 않는다.
-- 색인·트리거(0050·0051·0052)는 새 표에 모두 다시 만든다(내용은 옛 정의와 같다).

-- ---- notification_targets ----
CREATE TABLE notification_targets_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),
    type                 TEXT NOT NULL CHECK (type IN ('slack','teams','kakaowork','termspace')),
    secret_sealed        TEXT NOT NULL,
    hint                 TEXT NOT NULL DEFAULT '',
    kinds_json           TEXT NOT NULL DEFAULT '[]',
    active               INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    paused_reason        TEXT NOT NULL DEFAULT '' CHECK (paused_reason IN ('','auto')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_ok_at           TEXT NOT NULL DEFAULT '',
    last_error_code      TEXT NOT NULL DEFAULT '',
    last_error_at        TEXT NOT NULL DEFAULT '',
    created_by           INTEGER REFERENCES users(id),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);
INSERT INTO notification_targets_new (id, name, type, secret_sealed, hint, kinds_json, active, paused_reason, consecutive_failures, last_ok_at, last_error_code, last_error_at, created_by, created_at, updated_at)
    SELECT id, name, type, secret_sealed, hint, kinds_json, active, paused_reason, consecutive_failures, last_ok_at, last_error_code, last_error_at, created_by, created_at, updated_at FROM notification_targets ORDER BY id;
UPDATE sqlite_sequence SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'notification_targets') WHERE name = 'notification_targets_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'notification_targets');
DROP TABLE notification_targets;
ALTER TABLE notification_targets_new RENAME TO notification_targets;
CREATE TRIGGER trg_notification_targets_type_fixed BEFORE UPDATE OF type, created_at, created_by ON notification_targets
WHEN NEW.type <> OLD.type OR NEW.created_at <> OLD.created_at OR NEW.created_by IS NOT OLD.created_by
BEGIN SELECT RAISE(ABORT, '웹훅 대상의 종류는 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_notification_targets_no_delete BEFORE DELETE ON notification_targets
BEGIN SELECT RAISE(ABORT, '웹훅 대상은 지울 수 없습니다(사용 중지만 됩니다)'); END;

-- ---- notification_deliveries ----
CREATE TABLE notification_deliveries_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    notification_id INTEGER REFERENCES notifications(id),
    user_id         INTEGER REFERENCES users(id),
    target_id       INTEGER,
    channel         TEXT NOT NULL CHECK (channel IN ('email','slack','teams','kakaowork','termspace')),
    kind            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','held','sending','sent','failed','skipped')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    resends         INTEGER NOT NULL DEFAULT 0 CHECK (resends >= 0),
    next_try_at     TEXT NOT NULL,
    claimed_at      TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    digest_id       INTEGER,
    created_at      TEXT NOT NULL,
    sent_at         TEXT NOT NULL DEFAULT '',
    CHECK (user_id IS NOT NULL OR target_id IS NOT NULL)
);
INSERT INTO notification_deliveries_new (id, notification_id, user_id, target_id, channel, kind, status, attempts, resends, next_try_at, claimed_at, last_error_code, digest_id, created_at, sent_at)
    SELECT id, notification_id, user_id, target_id, channel, kind, status, attempts, resends, next_try_at, claimed_at, last_error_code, digest_id, created_at, sent_at FROM notification_deliveries ORDER BY id;
UPDATE sqlite_sequence SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'notification_deliveries') WHERE name = 'notification_deliveries_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'notification_deliveries');
DROP TABLE notification_deliveries;
ALTER TABLE notification_deliveries_new RENAME TO notification_deliveries;
CREATE UNIQUE INDEX idx_notification_deliveries_once ON notification_deliveries(COALESCE(notification_id, 0), COALESCE(user_id, 0), COALESCE(target_id, 0), channel) WHERE kind <> 'connection_test';
CREATE INDEX idx_notification_deliveries_due ON notification_deliveries(status, next_try_at);
CREATE INDEX idx_notification_deliveries_user ON notification_deliveries(user_id, channel, kind, status);
CREATE INDEX idx_notification_deliveries_created ON notification_deliveries(created_at);
CREATE INDEX idx_notification_deliveries_target ON notification_deliveries(target_id, status, next_try_at);
CREATE INDEX idx_notification_deliveries_held ON notification_deliveries(status, user_id) WHERE status = 'held';
CREATE TRIGGER trg_notification_deliveries_status BEFORE UPDATE OF status ON notification_deliveries
WHEN NEW.status <> OLD.status AND NOT (
    (OLD.status = 'queued'  AND NEW.status IN ('sending','skipped')) OR
    (OLD.status = 'held'    AND NEW.status IN ('queued','skipped')) OR
    (OLD.status = 'sending' AND NEW.status IN ('sent','queued','failed')) OR
    (OLD.status = 'failed'  AND NEW.status = 'queued'))
BEGIN SELECT RAISE(ABORT, '발송 건의 상태 전이가 올바르지 않습니다'); END;
CREATE TRIGGER trg_notification_deliveries_fixed BEFORE UPDATE ON notification_deliveries
WHEN NEW.notification_id IS NOT OLD.notification_id OR NEW.user_id IS NOT OLD.user_id OR NEW.target_id IS NOT OLD.target_id
  OR NEW.channel <> OLD.channel OR NEW.kind <> OLD.kind OR NEW.created_at <> OLD.created_at
BEGIN SELECT RAISE(ABORT, '발송 건의 알림·수신자·채널·종류는 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_notification_deliveries_no_delete BEFORE DELETE ON notification_deliveries
WHEN OLD.status IN ('queued','held','sending')
BEGIN SELECT RAISE(ABORT, '처리 중인 발송 건은 지울 수 없습니다'); END;
