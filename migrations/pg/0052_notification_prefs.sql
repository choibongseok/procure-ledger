-- 0052 (B36 단계 3): 수신 설정·묶음 설정. SQLite 0052_notification_prefs.sql 의 짝이다(초안은 pgbaseline -translate, 검토함).
CREATE TABLE notification_prefs (
    user_id    BIGINT NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    channel    TEXT NOT NULL CHECK (channel IN ('email')),
    mode       TEXT NOT NULL CHECK (mode IN ('on','off')),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (user_id, kind, channel)
);

CREATE TABLE notification_user_opts (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    digest     TEXT NOT NULL CHECK (digest IN ('on','off')),
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_notification_deliveries_held ON notification_deliveries(status, user_id) WHERE status = 'held';

INSERT INTO app_settings (key, value) VALUES
    ('notify.work_start','09:00'), ('notify.work_end','18:00'), ('notify.hold_max_hours','72'), ('notify.digest_default','0'),
    ('notify.payment_due_bd','3'), ('notify.close_check_start_bd','3'), ('notify.close_check_every_days','7') ON CONFLICT DO NOTHING;

