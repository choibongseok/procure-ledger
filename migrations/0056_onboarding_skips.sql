-- 0056: 도입 점검표의 "해당 없음" 표시(B38 3단계, 5·11절).
-- 점검표 상태는 DB 를 읽어 계산하고(수동 체크박스가 아니다), 사람이 정하는 것은 "이 회사에는 해당 없음"과 그 사유뿐이다.
-- 항목당 한 행이고(item_id), 표시·해제는 감사(onboarding.skip·onboarding.unskip)에 남는다. 비밀값·경로는 담지 않는다.
CREATE TABLE onboarding_skips (
    item_id TEXT PRIMARY KEY CHECK (length(item_id) BETWEEN 1 AND 40),
    reason  TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
    by_user INTEGER NOT NULL REFERENCES users(id),
    at      TEXT NOT NULL
);
