-- 0002: 결재 위임(B3, 구현 erp)과 관리자 설정 저장소(B2, 구현 platform·ai).
-- 시각은 모두 RFC3339 UTC 문자열("2006-01-02T15:04:05Z")로 저장한다(문자열 비교가 시간 순서와 같다).

-- ============================================================
-- [erp] 결재 위임: 위임자가 기간을 정해 수임자에게 결재를 맡긴다
-- ============================================================
CREATE TABLE approval_delegations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    delegator_id INTEGER NOT NULL REFERENCES users(id),   -- 위임자(원래 결재자)
    delegate_id  INTEGER NOT NULL REFERENCES users(id),   -- 수임자(대신 결재하는 사람)
    start_at    TEXT NOT NULL,
    end_at      TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT,                                    -- 해제 시각. NULL 이면 해제되지 않음
    CHECK (delegator_id <> delegate_id),                  -- 자기 자신에게 위임 금지
    CHECK (end_at > start_at)
);
CREATE INDEX idx_deleg_delegate  ON approval_delegations(delegate_id, start_at, end_at);
CREATE INDEX idx_deleg_delegator ON approval_delegations(delegator_id);

-- "누가 누구를 대신해 결재했는지": 이력 행은 불변이므로 컬럼만 더한다. NULL 이면 본인 결재.
ALTER TABLE approval_history ADD COLUMN on_behalf_of INTEGER REFERENCES users(id);
ALTER TABLE approval_steps   ADD COLUMN on_behalf_of INTEGER REFERENCES users(id);

-- 위임 결재의 최후 방어선(서비스 검사와 별개로 DB 가 막는다).
-- 1) 대리인이 곧 위임자일 수 없고, 2) 결재 시각에 유효한(기간 안, 해제되지 않은) 위임이 있어야 한다.
-- 요청자 본인이 결재하는 것은 trg_approval_no_self 가 actor_id 기준으로 막는다. 요청자가 위임자인 건
-- (요청자 외 결재자가 없을 때 자기 단계를 타 부서 결재자에게 위임)은 수임자가 결재하므로 허용한다.
CREATE TRIGGER trg_approval_delegated_rules BEFORE INSERT ON approval_history
WHEN NEW.on_behalf_of IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, '위임 결재는 승인·반려에만 쓸 수 있습니다')
    WHERE NEW.action NOT IN ('approve','reject');
    SELECT RAISE(ABORT, '본인을 대신해 결재할 수 없습니다')
    WHERE NEW.on_behalf_of = NEW.actor_id;
    SELECT RAISE(ABORT, '유효한 위임이 없습니다')
    WHERE NOT EXISTS (
        SELECT 1 FROM approval_delegations d
        WHERE d.delegator_id = NEW.on_behalf_of AND d.delegate_id = NEW.actor_id
          AND d.start_at <= NEW.at AND NEW.at < d.end_at
          AND (d.revoked_at IS NULL OR d.revoked_at > NEW.at));
END;

-- 위임 행도 지우지 못한다(해제는 revoked_at 설정). 기간·당사자는 바꿀 수 없다.
CREATE TRIGGER trg_deleg_no_delete BEFORE DELETE ON approval_delegations
BEGIN SELECT RAISE(ABORT, 'approval_delegations 는 삭제할 수 없습니다'); END;
CREATE TRIGGER trg_deleg_immutable BEFORE UPDATE ON approval_delegations
WHEN NEW.delegator_id <> OLD.delegator_id OR NEW.delegate_id <> OLD.delegate_id
  OR NEW.start_at <> OLD.start_at OR NEW.end_at <> OLD.end_at
  OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NOT OLD.revoked_at)
BEGIN SELECT RAISE(ABORT, '위임의 당사자·기간은 수정할 수 없고, 해제는 한 번만 됩니다'); END;

-- ============================================================
-- [platform] 관리자 설정 저장소 (키-값). 변경은 admin 만, 감사 로그(setting.update)에 남는다.
-- ============================================================
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_by INTEGER,
    updated_at TEXT
);
-- B2: LLM 으로 이미지 증빙을 보내도 되는가. 기본 꺼짐. 행이 없어도 꺼짐으로 읽는다.
INSERT INTO app_settings (key, value) VALUES ('llm.image_transmission_enabled', 'false');
