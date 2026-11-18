-- 0050: 알림 채널 단계 1 - 발송 큐·기본값(B36, 6절). (0048 B35 묶음, 0049 B35 도메인.)
--  - notification_deliveries: 알림 이벤트가 받는 사람 × 채널마다 만드는 큐 행. **메시지 본문·주소를 저장하지 않는다**(발송 직전에 틀로 만든다, D13).
--    상태 전이는 queued → sending → sent | queued(재시도) | failed, queued → skipped, failed → queued(다시 보내기), held → queued | skipped. 표에 없는 전이는 트리거가 거부한다.
--    같은 알림·사람·채널은 한 건만 만든다(유일 색인). target_id 는 웹훅 대상(2단계)을 위해 비워 둔 자리다.
--    삭제는 끝난 건(sent·failed·skipped)만 가능하다(보존 정리 작업).
--  - notification_defaults: 관리자 기본값(종류 × 채널). 시드는 명세 4.8. 행이 없는 종류는 외부로 보내지 않는다. locked 는 필수 알림 자리(지금은 쓰지 않는다).
--  - app_settings 키: 합치기 창·재시도 횟수·분당/사용자별 상한·보존 일수(초기값, 관리자 설정 화면은 후속).
CREATE TABLE notification_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    notification_id INTEGER REFERENCES notifications(id),
    user_id         INTEGER REFERENCES users(id),
    target_id       INTEGER,
    channel         TEXT NOT NULL CHECK (channel IN ('email','slack','teams','kakaowork')),
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
CREATE UNIQUE INDEX idx_notification_deliveries_once ON notification_deliveries(COALESCE(notification_id, 0), COALESCE(user_id, 0), COALESCE(target_id, 0), channel);
CREATE INDEX idx_notification_deliveries_due ON notification_deliveries(status, next_try_at);
CREATE INDEX idx_notification_deliveries_user ON notification_deliveries(user_id, channel, kind, status);
CREATE INDEX idx_notification_deliveries_created ON notification_deliveries(created_at);

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

CREATE TABLE notification_defaults (
    kind       TEXT NOT NULL,
    channel    TEXT NOT NULL CHECK (channel IN ('email')),
    enabled    INTEGER NOT NULL CHECK (enabled IN (0,1)),
    locked     INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0,1)),
    updated_by INTEGER REFERENCES users(id),
    updated_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (kind, channel)
);
INSERT INTO notification_defaults (kind, channel, enabled) VALUES
    ('approval_pending','email',1), ('approval_rejected','email',1), ('agreement_objected','email',1),
    ('approval_done','email',0), ('submission_cancelled','email',0),
    ('document_cc','email',0), ('receipt_returned','email',0), ('receipt_done','email',0),
    ('receipt_arrived','email',1), ('approval_nudge','email',1), ('approval_reassigned','email',1),
    ('absence_assigned','email',0), ('absence_started','email',0), ('absence_ended','email',0), ('absence_revoked','email',0),
    ('card_reminder','email',1), ('card_overdue_head','email',1), ('card_returned','email',1), ('card_reopened','email',1),
    ('payment_due','email',1), ('month_close_check','email',1);

INSERT OR IGNORE INTO app_settings (key, value) VALUES
    ('notify.window_sec','60'), ('notify.max_attempts','5'), ('notify.mail_per_min','60'),
    ('notify.mail_per_user_hour','30'), ('notify.keep_days','365');
