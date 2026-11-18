-- 0067 (B42 2단계 보강, 명세 9.6·9.4): 합치기 건수와 자동 중지 알림 종류의 메일 기본값. (명세 초안의 0067_connector_runs 는 3단계라 0068 로 밀린다.)
--  - webhook_deliveries.coalesced: 창 안에서 이 건에 합쳐진 앞 건의 수(X-PL-Coalesced 헤더). 합쳐진 앞 건은 skipped(사유 coalesced)로 남는다.
--  - 알림 종류 webhook_paused(notify.Kinds 에 같은 코드): 연속 실패로 구독이 자동 중지되면 webhook:manage 권한자에게 간다. 메일 기본 켜짐.
ALTER TABLE webhook_deliveries ADD COLUMN coalesced INTEGER NOT NULL DEFAULT 0 CHECK (coalesced >= 0);
INSERT OR IGNORE INTO notification_defaults (kind, channel, enabled, locked) VALUES ('webhook_paused','email',1,0);
