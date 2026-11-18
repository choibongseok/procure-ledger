-- 0015: 지출 결의서 양식 2판(B23 단계 1 검증 지적). 적용된 0013 은 고치지 않고 새 게시 버전을 더한다.
--  1) 금액은 지출 내역 표의 금액 합계와 같아야 한다(sumOf). 금액이 결재선 금액이라 작게 적어 결재선을 줄이는 것을 서버가 막는다.
--  2) 기본 수신처 조직 코드를 개발 시드의 재무팀 코드(T-FIN)에 맞춘다(운영은 양식 관리 화면에서 실제 코드로 바꾼다).
-- 기안한 문서는 당시 버전(1판)으로 계속 진행되고, 새 기안은 최신 게시 버전(2판)을 쓴다.
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT 3, 2, 'published',
       replace(replace(definition,
           '{"key":"total","label":"금액","type":"amount","required":true}',
           '{"key":"total","label":"금액","type":"amount","required":true,"sumOf":"items.amount"}'),
           '"unit_code":"FIN"', '"unit_code":"T-FIN"'),
       strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
  FROM approval_form_versions WHERE form_id = 3 AND version = 1;

UPDATE approval_form_versions SET status = 'retired' WHERE form_id = 3 AND version = 1;
