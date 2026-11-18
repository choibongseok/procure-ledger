-- 0052: 알림 채널 단계 3 - 사용자별 수신 설정·업무 시간 외 묶음·신규 이벤트 설정(B36 명세 4.3·4.4, 3.2 의 payment_due·month_close_check).
--  - notification_prefs: 사람 × 알림 종류 × 채널(메일)의 "켜짐/꺼짐". 행이 없으면 "기본값 따름"(관리자가 정한 notification_defaults)이다.
--    앱 안 알림은 끌 수 없으므로(D3) 앱 채널 행은 없다. 필수 알림(notification_defaults.locked)은 꺼도 무시된다.
--  - notification_user_opts: 업무 시간 외 메일 묶음 선택. 행이 없으면 관리자 기본값(app_settings notify.digest_default)을 따른다.
--    본인 수신 설정 변경은 개인 상태라 감사에 남기지 않는다(읽음 처리와 같은 원칙).
--  - app_settings 키(초기값, 관리자가 바꿀 수 있다): 업무 시간·묶음 보류 상한·묶음 기본값, 지급 예정 영업일 수, 월 마감 점검 시작 영업일·주기.
CREATE TABLE notification_prefs (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    channel    TEXT NOT NULL CHECK (channel IN ('email')),
    mode       TEXT NOT NULL CHECK (mode IN ('on','off')),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (user_id, kind, channel)
);

CREATE TABLE notification_user_opts (
    user_id    INTEGER PRIMARY KEY REFERENCES users(id),
    digest     TEXT NOT NULL CHECK (digest IN ('on','off')),
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_notification_deliveries_held ON notification_deliveries(status, user_id) WHERE status = 'held';

INSERT OR IGNORE INTO app_settings (key, value) VALUES
    ('notify.work_start','09:00'), ('notify.work_end','18:00'), ('notify.hold_max_hours','72'), ('notify.digest_default','0'),
    ('notify.payment_due_bd','3'), ('notify.close_check_start_bd','3'), ('notify.close_check_every_days','7');
