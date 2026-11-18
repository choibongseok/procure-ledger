-- 0027: 월 마감과 재고 평가 단계 3(B27 ③, 3.3·6절): 시스템 양식 "월 마감 해제 요청서"(MCR).
-- 일반 문서 작성 경로로는 만들 수 없고(코드의 guardSystemForm), 월 마감 화면의 "마감 해제 요청"으로만 만들어진다. 결재선은 재무팀장 단독 결재(재무팀 조직 코드 T-FIN 의 조직장)이고
-- 참조는 감사팀(T-AUD)이다. 조직 코드가 양식에 고정되는 한계는 재고 조정 전표(ADJ)와 같고, 운영은 새 양식 버전을 게시해 바꾼다.
-- 최종 승인 때 같은 트랜잭션의 후속 동작(docHookTable 의 MCR)이 월을 열림으로 바꾼다.
INSERT INTO approval_forms (code, category, name, kind, active, required_permission, created_at)
VALUES ('MCR', 'other', '월 마감 해제 요청서', 'general', 1, 'inventory:close', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at)
SELECT id, 1, 'published',
 '{"system":true,"fields":[{"key":"month","label":"해제할 월","type":"text","required":true,"maxLength":7},{"key":"reason","label":"해제 사유","type":"textarea","required":true,"maxLength":500},{"key":"summary","label":"영향 요약","type":"textarea","required":false,"maxLength":2000}],"body":"","line":{"steps":[{"role":"approve","source":"unit_head","unitCode":"T-FIN","required":true}]},"recipients":{"cc":[{"unit_code":"T-AUD"}],"receive":[]},"numbering":{"pattern":"MCR-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":10,"dept_limit":[],"settings":{"line_edit":"none","objection_action":"record","amount_field":"","dept_visible":false,"urgent_allowed":false}}',
 strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
 FROM approval_forms WHERE code = 'MCR';
