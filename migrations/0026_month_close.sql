-- 0026: 월 마감과 재고 평가 단계 2(B27 ②, 3.2·6절): 월 마감 상태·사건·월말 스냅샷과 마감 월 쓰기 잠금 트리거.
--  1) month_closes: 월마다 한 줄(열림·마감됨, 마감 버전). month_close_events: 마감·해제 사건(추가 전용).
--  2) month_close_lines: 마감 버전별 품목·창고별 월말 스냅샷(추가 전용, 재마감하면 새 버전으로 더하고 이전 버전은 보존).
--  3) 마감된 월의 장부일을 가진 쓰기는 서버(assertMonthOpen)가 거부하고, 서버 경로를 우회해도 아래 트리거가 같은 문구로 중단한다.
CREATE TABLE month_closes (
    month       TEXT PRIMARY KEY CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    status      TEXT NOT NULL CHECK (status IN ('open','closed')),
    version     INTEGER NOT NULL CHECK (version >= 1),
    closed_by   INTEGER REFERENCES users(id),
    closed_at   TEXT,
    released_at TEXT,
    released_doc_id INTEGER
);

CREATE TABLE month_close_events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    month    TEXT NOT NULL,
    kind     TEXT NOT NULL CHECK (kind IN ('close','release_request','release','release_reject')),
    version  INTEGER NOT NULL,
    actor_id INTEGER NOT NULL,
    doc_id   INTEGER,
    at       TEXT NOT NULL
);
CREATE INDEX idx_month_close_events_month ON month_close_events(month, id);
CREATE TRIGGER trg_month_close_events_no_update BEFORE UPDATE ON month_close_events BEGIN SELECT RAISE(ABORT, '월 마감 사건은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_month_close_events_no_delete BEFORE DELETE ON month_close_events BEGIN SELECT RAISE(ABORT, '월 마감 사건은 지울 수 없습니다'); END;

CREATE TABLE month_close_lines (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    month           TEXT NOT NULL,
    version         INTEGER NOT NULL,
    item_id         INTEGER NOT NULL REFERENCES items(id),
    warehouse_id    INTEGER NOT NULL REFERENCES warehouses(id),
    open_qty        INTEGER NOT NULL, open_amount     INTEGER NOT NULL,
    in_qty          INTEGER NOT NULL, in_amount       INTEGER NOT NULL,
    out_qty         INTEGER NOT NULL, out_amount      INTEGER NOT NULL,
    transfer_qty    INTEGER NOT NULL, transfer_amount INTEGER NOT NULL,
    adjust_qty      INTEGER NOT NULL, adjust_amount   INTEGER NOT NULL,
    close_qty       INTEGER NOT NULL, close_amount    INTEGER NOT NULL,
    avg_cost        INTEGER NOT NULL,
    UNIQUE (month, version, item_id, warehouse_id)
);
CREATE TRIGGER trg_month_close_lines_no_update BEFORE UPDATE ON month_close_lines BEGIN SELECT RAISE(ABORT, '월말 스냅샷은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_month_close_lines_no_delete BEFORE DELETE ON month_close_lines BEGIN SELECT RAISE(ABORT, '월말 스냅샷은 지울 수 없습니다'); END;

-- 마감된 월 쓰기 잠금(방어선). 월은 장부일의 'YYYY-MM' 이다(원장은 book_date, 없으면 기록한 날의 KST 날짜).
CREATE TRIGGER trg_ledger_month_open BEFORE INSERT ON stock_ledger
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(COALESCE(NEW.book_date, date(NEW.created_at, '+9 hours')), 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_issue_month_open BEFORE INSERT ON issue_slips
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.issue_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_count_month_open_ins BEFORE INSERT ON stock_counts
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.base_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

CREATE TRIGGER trg_count_month_open_upd BEFORE UPDATE OF base_date ON stock_counts
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(NEW.base_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;

-- 마감된 달의 평가 줄은 다시 계산할 수 없다(열린 달만 소급 재계산 대상이다).
CREATE TRIGGER trg_valuation_month_open_upd BEFORE UPDATE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
CREATE TRIGGER trg_valuation_month_open_del BEFORE DELETE ON stock_valuation
WHEN EXISTS (SELECT 1 FROM month_closes WHERE status = 'closed' AND month = substr(OLD.book_date, 1, 7))
BEGIN SELECT RAISE(ABORT, '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.'); END;
