-- 0022: 구매 기능 깊이 단계 1(B25 ①, 6절): 부서·월 예산, 상신 때 예산 판정 스냅샷, 구매요청 희망 납기·납품처.
--  1) budgets: 부서·월 한 쌍의 월 예산(공급가액, 원). 삭제는 없다. budget_revisions: 등록·수정 이력(추가 전용).
--  2) purchase_requests: 희망 납기(due_date, 선택)·납품처(delivery_place), 상신 때 판정한 예산 월·상태·스냅샷(budget_json). 기존 요청은 빈 값으로 그대로 동작한다.
--  3) purchase_orders: 발주 생성 때 요청에서 복사하는 납기·납품처(스냅샷).
CREATE TABLE budgets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    department_id INTEGER NOT NULL REFERENCES departments(id),
    month         TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    amount        INTEGER NOT NULL CHECK (amount >= 0),
    updated_by    INTEGER NOT NULL REFERENCES users(id),
    updated_at    TEXT NOT NULL,
    UNIQUE (department_id, month)
);
CREATE TRIGGER trg_budgets_no_delete BEFORE DELETE ON budgets BEGIN SELECT RAISE(ABORT, '예산은 지울 수 없습니다'); END;

CREATE TABLE budget_revisions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    budget_id  INTEGER NOT NULL REFERENCES budgets(id),
    old_amount INTEGER,
    new_amount INTEGER NOT NULL CHECK (new_amount >= 0),
    reason     TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
    actor_id   INTEGER NOT NULL REFERENCES users(id),
    at         TEXT NOT NULL
);
CREATE INDEX idx_budget_revisions_budget ON budget_revisions(budget_id, id);
CREATE TRIGGER trg_budget_revisions_no_update BEFORE UPDATE ON budget_revisions BEGIN SELECT RAISE(ABORT, '예산 수정 이력은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_budget_revisions_no_delete BEFORE DELETE ON budget_revisions BEGIN SELECT RAISE(ABORT, '예산 수정 이력은 지울 수 없습니다'); END;

ALTER TABLE purchase_requests ADD COLUMN due_date TEXT;
ALTER TABLE purchase_requests ADD COLUMN delivery_place TEXT NOT NULL DEFAULT '본사 창고';
ALTER TABLE purchase_requests ADD COLUMN budget_month TEXT;
ALTER TABLE purchase_requests ADD COLUMN budget_state TEXT NOT NULL DEFAULT 'none' CHECK (budget_state IN ('none','within','over','unregistered'));
ALTER TABLE purchase_requests ADD COLUMN budget_json TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_pr_budget ON purchase_requests(department_id, budget_month, status);

ALTER TABLE purchase_orders ADD COLUMN due_date TEXT;
ALTER TABLE purchase_orders ADD COLUMN delivery_place TEXT NOT NULL DEFAULT '본사 창고';
