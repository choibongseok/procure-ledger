-- 0038: 전표 승인 요청서 시스템 양식 JV(B31 3.2.4,).
-- 일반 문서 작성으로는 만들 수 없고(코드의 guardSystemForm) 전표 화면의 "승인 요청"으로만 만들어진다. 결재선은 재무팀장 단독(재무팀 조직 코드 T-FIN 의 조직장), 참조는 감사팀(T-AUD).
-- 조직 코드가 양식에 고정되는 한계는 ADJ·MCR 과 같고 운영은 새 양식 버전을 게시해 바꾼다. 합계(차변)가 1억원 이상이면 재무본부장(경영지원본부장, 조직 코드 DIV-MG) 단계가 더해진다(단계의 minAmount, B31 D6).
-- 최종 승인 때 같은 트랜잭션의 후속 동작(docHookTable 의 JV)이 전표를 승인 상태로 바꾸고 채무 항목을 만든다.
INSERT INTO approval_forms (code, category, name, kind, active, required_permission, created_at)
VALUES ('JV', 'finance', '전표 승인 요청서', 'general', 1, 'ledger:write', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT id, 1, 'published',
 '{"system":true,"fields":[{"key":"journal_no","label":"전표 번호","type":"text","required":true,"maxLength":20},{"key":"posting_date","label":"전기일","type":"text","required":true,"maxLength":10},{"key":"memo","label":"적요","type":"text","required":false,"maxLength":200},{"key":"total","label":"차변 합계(원)","type":"amount","required":true},{"key":"summary","label":"줄 요약","type":"textarea","required":false,"maxLength":5000}],"body":"","line":{"steps":[{"role":"approve","source":"unit_head","unitCode":"T-FIN","required":true},{"role":"approve","source":"unit_head","unitCode":"DIV-MG","required":true,"minAmount":100000000}]},"recipients":{"cc":[{"unit_code":"T-AUD"}],"receive":[]},"numbering":{"pattern":"JVD-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":10,"dept_limit":[],"settings":{"line_edit":"none","objection_action":"record","amount_field":"total","dept_visible":false,"urgent_allowed":false}}',
 strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
 FROM approval_forms WHERE code = 'JV';
