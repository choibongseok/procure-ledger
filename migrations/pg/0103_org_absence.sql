-- 0103 (B50 12.4): SQLite 0103_org_absence.sql 의 짝이다. 사람 구간 표의 휴직 구분 열(빈 값 = 휴직 아님).
ALTER TABLE org_person_spans ADD COLUMN absence_kind TEXT NOT NULL DEFAULT ''
    CHECK (absence_kind IN ('','illness','childcare','family_care','personal','other'));
