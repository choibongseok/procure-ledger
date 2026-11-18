-- 0067 (B42 2단계 보강): SQLite 0067_webhook_coalesce.sql 의 짝이다.
ALTER TABLE webhook_deliveries ADD COLUMN coalesced BIGINT NOT NULL DEFAULT 0 CHECK (coalesced >= 0);
INSERT INTO notification_defaults (kind, channel, enabled, locked) VALUES ('webhook_paused','email',1,0) ON CONFLICT DO NOTHING;
