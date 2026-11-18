-- 0051: 알림 채널 단계 2 - 웹훅 대상(슬랙·MS 팀즈·카카오워크, B36 명세 4.6·6절). 채널(방) 단위 공용 발송이다(D2).
--  - notification_targets: 관리자가 등록한 웹훅 대상. 주소(와 카카오워크의 앱 키·대화방 ID)는 봉인한 값 하나(secret_sealed)로만 저장한다 —
--    AES-256-GCM, 키 PL_ACCOUNT_SECRET_KEY, 용도별 AAD 로 계좌번호 칸과 섞이지 않는다(D5). 평문은 어디에도 없고 hint 에는 호스트와 끝 6자만 있다.
--    삭제는 없다(사용 중지만, 발송 기록 보존). 종류(type)는 바꿀 수 없다(허용 호스트가 종류별이다).
--    effective 사용 가능 = active = 1 AND paused_reason = ''. paused_reason 'auto' 는 연속 실패로 자동 중지된 상태다(시험 발송 성공이나 재개로 풀린다).
--  - 발송 건 유일 색인은 시험 발송 건(connection_test)을 뺀 부분 색인으로 바꾼다(같은 대상에 시험을 여러 번 보낼 수 있다).
--  - app_settings: notify.extra_hosts(허용 호스트 추가, 쉼표 구분), notify.target_per_min(대상별 분당 발송 상한), notify.target_fail_pause(연속 실패 자동 중지 횟수).
CREATE TABLE notification_targets (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),
    type                 TEXT NOT NULL CHECK (type IN ('slack','teams','kakaowork')),
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
CREATE TRIGGER trg_notification_targets_type_fixed BEFORE UPDATE OF type, created_at, created_by ON notification_targets
WHEN NEW.type <> OLD.type OR NEW.created_at <> OLD.created_at OR NEW.created_by IS NOT OLD.created_by
BEGIN SELECT RAISE(ABORT, '웹훅 대상의 종류는 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_notification_targets_no_delete BEFORE DELETE ON notification_targets
BEGIN SELECT RAISE(ABORT, '웹훅 대상은 지울 수 없습니다(사용 중지만 됩니다)'); END;

DROP INDEX idx_notification_deliveries_once;
CREATE UNIQUE INDEX idx_notification_deliveries_once ON notification_deliveries(COALESCE(notification_id, 0), COALESCE(user_id, 0), COALESCE(target_id, 0), channel) WHERE kind <> 'connection_test';
CREATE INDEX idx_notification_deliveries_target ON notification_deliveries(target_id, status, next_try_at);

INSERT OR IGNORE INTO app_settings (key, value) VALUES
    ('notify.extra_hosts',''), ('notify.target_per_min','20'), ('notify.target_fail_pause','5');
