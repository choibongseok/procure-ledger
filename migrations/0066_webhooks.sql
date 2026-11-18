-- 0066 (B42 2단계, 5절·9절): 변경 이벤트 웹훅 구독·발송 건.(발송), platform(구독 화면·권한).
--  - webhook_subscriptions: 주소와 서명 비밀(과 교체 중인 옛 비밀·만료 시각)을 JSON 한 덩어리로 봉인해 secret_sealed 에만 둔다
--    (acctsecret.SealFor("webhook-sub"), 계좌번호·알림 웹훅 주소 칸과 섞이지 않는다). 평문은 화면·응답·감사·기록 어디에도 없고 hint 에는 호스트와 끝 6자만 있다.
--    삭제는 없다(사용 중지만, 발송 기록 보존). cursor_seq 는 이 구독이 변경 피드에서 발송 건으로 만든 마지막 번호다(새 구독은 만든 시점의 head 부터).
--  - webhook_deliveries: 이벤트(변경 피드 줄) 하나를 구독 하나에 보내는 일. 같은 (구독, 이벤트)는 한 건이다. 피드 줄의 자원·번호·동작·상태를 복사해 둔다
--    (피드 보존 정리와 독립이고, 객체 단위 순서 보장 조회가 피드를 읽지 않게). 본문·주소·응답 본문은 저장하지 않는다(사유 코드만).
--    상태 전이: queued → sending | skipped, sending → sent | queued(재시도) | failed, failed → queued(다시 보내기). 표에 없는 전이는 트리거가 거부한다.
CREATE TABLE webhook_subscriptions (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),
    secret_sealed        TEXT NOT NULL,
    hint                 TEXT NOT NULL DEFAULT '',
    events_json          TEXT NOT NULL DEFAULT '[]',
    include_api          INTEGER NOT NULL DEFAULT 0 CHECK (include_api IN (0,1)),
    active               INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    paused_reason        TEXT NOT NULL DEFAULT '' CHECK (paused_reason IN ('','auto','manual')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    cursor_seq           INTEGER NOT NULL DEFAULT 0 CHECK (cursor_seq >= 0),
    last_ok_at           TEXT NOT NULL DEFAULT '',
    last_error_code      TEXT NOT NULL DEFAULT '',
    last_error_at        TEXT NOT NULL DEFAULT '',
    created_by           INTEGER REFERENCES users(id),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);
CREATE TRIGGER trg_webhook_subscriptions_no_delete BEFORE DELETE ON webhook_subscriptions
BEGIN SELECT RAISE(ABORT, '웹훅 구독은 지울 수 없습니다(사용 중지만 됩니다)'); END;

CREATE TABLE webhook_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id INTEGER NOT NULL REFERENCES webhook_subscriptions(id),
    event_seq       INTEGER NOT NULL,
    resource        TEXT NOT NULL,
    object_id       INTEGER NOT NULL,
    event_type      TEXT NOT NULL,                 -- 자원.동작(예: journal.status_changed)
    event_status    TEXT NOT NULL DEFAULT '',      -- 새 상태 코드(상태가 있는 자원만)
    event_at        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sending','sent','failed','skipped')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    resends         INTEGER NOT NULL DEFAULT 0 CHECK (resends >= 0),
    next_try_at     TEXT NOT NULL,
    claimed_at      TEXT NOT NULL DEFAULT '',
    last_try_at     TEXT NOT NULL DEFAULT '',
    last_code       TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    sent_at         TEXT NOT NULL DEFAULT '',
    UNIQUE (subscription_id, event_seq)
);
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries(status, next_try_at);
CREATE INDEX idx_webhook_deliveries_object ON webhook_deliveries(subscription_id, resource, object_id, event_seq);
CREATE INDEX idx_webhook_deliveries_sub ON webhook_deliveries(subscription_id, status);
CREATE TRIGGER trg_webhook_deliveries_status BEFORE UPDATE OF status ON webhook_deliveries
WHEN NEW.status <> OLD.status AND NOT (
    (OLD.status = 'queued'  AND NEW.status IN ('sending','skipped')) OR
    (OLD.status = 'sending' AND NEW.status IN ('sent','queued','failed')) OR
    (OLD.status = 'failed'  AND NEW.status = 'queued'))
BEGIN SELECT RAISE(ABORT, '웹훅 발송 건의 상태 전이가 올바르지 않습니다'); END;
CREATE TRIGGER trg_webhook_deliveries_fixed BEFORE UPDATE ON webhook_deliveries
WHEN NEW.subscription_id <> OLD.subscription_id OR NEW.event_seq <> OLD.event_seq OR NEW.resource <> OLD.resource OR NEW.object_id <> OLD.object_id
  OR NEW.event_type <> OLD.event_type OR NEW.event_status <> OLD.event_status OR NEW.event_at <> OLD.event_at OR NEW.created_at <> OLD.created_at
BEGIN SELECT RAISE(ABORT, '웹훅 발송 건의 구독·이벤트는 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_webhook_deliveries_no_delete BEFORE DELETE ON webhook_deliveries
WHEN OLD.status IN ('queued','sending')
BEGIN SELECT RAISE(ABORT, '처리 중인 웹훅 발송 건은 지울 수 없습니다'); END;

INSERT OR IGNORE INTO app_settings (key, value) VALUES
    ('webhook.allow_hosts',''), ('webhook.extra_ports',''), ('webhook.per_min','60'), ('webhook.keep_days','365');
