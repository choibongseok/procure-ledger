-- 0056 (B38 3단계): 도입 점검표 해당 없음. SQLite 0056_onboarding_skips.sql 의 짝이다(초안은 pgbaseline -translate, 검토함).
CREATE TABLE onboarding_skips (
    item_id TEXT PRIMARY KEY CHECK (length(item_id) BETWEEN 1 AND 40),
    reason  TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
    by_user BIGINT NOT NULL REFERENCES users(id),
    at      TEXT NOT NULL
);

