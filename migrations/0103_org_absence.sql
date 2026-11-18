-- 0103 (B50 12.4): 휴직·복직. 사람 구간 표에 휴직 구분 열을 더한다(빈 값 = 휴직 아님).
-- 표를 다시 만들지 않는다: 쓰기 보호 트리거(org_write_guard)와 기존 구간 행이 그대로 남는다.
-- 이벤트 로그가 정본이라 기존 설치는 휴직 이벤트가 없어 모든 행이 '' 이고, 구간을 다시 계산한 결과와 같다.
-- org_events.kind 는 DB CHECK 가 없어 새 이벤트 종류(absence_start·absence_end)에 표 변경이 필요 없다.
ALTER TABLE org_person_spans ADD COLUMN absence_kind TEXT NOT NULL DEFAULT ''
    CHECK (absence_kind IN ('','illness','childcare','family_care','personal','other'));
