-- 0099 (B50 11절): 개인정보 열람 기록 범주에 'attendance' 를 더한다. SQLite 0099_privacy_attendance_category.sql 의 짝이다.
-- PG 는 CHECK 제약만 바꾸면 된다(표를 다시 만들지 않으므로 행·해시 체인·추가 전용 트리거·파기 구간이 그대로다).
ALTER TABLE privacy_access_log DROP CONSTRAINT privacy_access_log_category_check;
ALTER TABLE privacy_access_log ADD CONSTRAINT privacy_access_log_category_check CHECK (category IN ('contact','account','card','employee_no','attendance'));
