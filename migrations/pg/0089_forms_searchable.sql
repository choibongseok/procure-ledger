-- 0089 (B47 1단계): SQLite 0089_forms_searchable.sql 의 짝이다. 양식별 "내용 검색에 포함". 0 이면 그 양식의 완료 문서는 내용 검색에 나오지 않는다(기존 문서도 즉시, 조회 때 확인한다).
-- 업로드 표(knowledge_uploads)는 2단계에서 0090 으로 더한다(1단계에는 쓰는 곳이 없다).
ALTER TABLE approval_forms ADD COLUMN searchable BIGINT NOT NULL DEFAULT 1 CHECK (searchable IN (0,1));
