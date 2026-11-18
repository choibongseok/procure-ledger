-- 0028: 월 마감 상태 표(month_closes)의 직접 수정 방어(B27 검증자 B1). 스냅샷·사건 표처럼 서버 경로를 우회한 UPDATE·DELETE 도 막는다.
--  - 지우기는 모두 거부한다.
--  - 새 줄은 "마감됨, 버전 1" 로만 들어간다(처음 마감).
--  - 고치기는 두 전이만 허용한다: 열림 → 마감됨(버전 +1, 마감 일시 있음), 마감됨 → 열림(버전·마감자·마감 일시 그대로, 해제 일시 있음). 월 키는 바뀌지 않는다.
CREATE TRIGGER trg_month_closes_no_delete BEFORE DELETE ON month_closes
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 지울 수 없습니다'); END;

CREATE TRIGGER trg_month_closes_insert_guard BEFORE INSERT ON month_closes
WHEN NOT (NEW.status = 'closed' AND NEW.version = 1 AND NEW.closed_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 처음 마감(버전 1)으로만 만들 수 있습니다'); END;

CREATE TRIGGER trg_month_closes_update_guard BEFORE UPDATE ON month_closes
WHEN NOT (
  NEW.month = OLD.month AND (
    (OLD.status = 'open' AND NEW.status = 'closed' AND NEW.version = OLD.version + 1 AND NEW.closed_at IS NOT NULL)
    OR
    (OLD.status = 'closed' AND NEW.status = 'open' AND NEW.version = OLD.version AND NEW.closed_by IS OLD.closed_by AND NEW.closed_at IS OLD.closed_at AND NEW.released_at IS NOT NULL)
  )
)
BEGIN SELECT RAISE(ABORT, '월 마감 상태는 마감·해제 절차로만 바꿀 수 있습니다'); END;
