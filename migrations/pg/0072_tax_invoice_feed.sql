-- 0072 (B43 3단계): 변경 피드 자원 'tax_invoice'·전표 출처 'tax_invoice'. SQLite 0072_tax_invoice_feed.sql 의 짝이다.
-- PG 는 제약만 바꾼다(표를 다시 만들지 않는다). 기존 행·번호·추가 전용 트리거에 영향이 없다.
ALTER TABLE change_events DROP CONSTRAINT change_events_resource_check;
ALTER TABLE change_events ADD CONSTRAINT change_events_resource_check CHECK (resource IN ('item','vendor','purchase_order','receipt','journal','payable','tax_invoice'));
ALTER TABLE journals DROP CONSTRAINT journals_source_type_check;
ALTER TABLE journals ADD CONSTRAINT journals_source_type_check CHECK (source_type IN ('','verdict','approval_doc','payment','card_txn','api','tax_invoice'));
