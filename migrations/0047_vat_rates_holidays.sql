-- 0047: 부가세율 효력일 이력과 공휴일(B34 단계 2·3,).
--  - vat_rates: 효력일(effective_from)부터 적용하는 세율(정수 %). 시드는 효력일 0001-01-01, 세율 10(현행 10% 와 같다).
--    수정·삭제는 없다(취소 칸만 0→1). 같은 효력일은 취소되지 않은 행끼리 하나만 있다.
--  - holidays: 영업일 달력의 공휴일(토·일은 자동 휴일이라 넣지 않는다). 수정은 없고, 삭제는 오늘(KST) 이후 날짜만 된다.
CREATE TABLE vat_rates (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    effective_from TEXT NOT NULL CHECK (effective_from GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    rate_percent   INTEGER NOT NULL CHECK (rate_percent BETWEEN 0 AND 100),
    created_by     INTEGER REFERENCES users(id),
    created_at     TEXT NOT NULL,
    canceled       INTEGER NOT NULL DEFAULT 0 CHECK (canceled IN (0,1)),
    canceled_by    INTEGER REFERENCES users(id),
    canceled_at    TEXT
);
CREATE UNIQUE INDEX idx_vat_rates_effective ON vat_rates(effective_from) WHERE canceled = 0;
INSERT INTO vat_rates (effective_from, rate_percent, created_at) VALUES ('0001-01-01', 10, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
CREATE TRIGGER trg_vat_rates_no_delete BEFORE DELETE ON vat_rates BEGIN SELECT RAISE(ABORT, '부가세율 이력은 지울 수 없습니다'); END;
CREATE TRIGGER trg_vat_rates_fixed BEFORE UPDATE ON vat_rates
WHEN NEW.effective_from IS NOT OLD.effective_from OR NEW.rate_percent IS NOT OLD.rate_percent OR NEW.created_at IS NOT OLD.created_at OR NEW.created_by IS NOT OLD.created_by OR NEW.canceled < OLD.canceled
BEGIN SELECT RAISE(ABORT, '부가세율 이력은 고칠 수 없습니다(취소만 됩니다)'); END;

CREATE TABLE holidays (
    date       TEXT PRIMARY KEY CHECK (date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    created_by INTEGER REFERENCES users(id),
    created_at TEXT NOT NULL
);
CREATE TRIGGER trg_holidays_no_update BEFORE UPDATE ON holidays BEGIN SELECT RAISE(ABORT, '공휴일은 고칠 수 없습니다(지우고 다시 등록하세요)'); END;
CREATE TRIGGER trg_holidays_future_only_delete BEFORE DELETE ON holidays
WHEN OLD.date <= date('now', '+9 hours')
BEGIN SELECT RAISE(ABORT, '지난 날짜의 공휴일은 지울 수 없습니다'); END;
