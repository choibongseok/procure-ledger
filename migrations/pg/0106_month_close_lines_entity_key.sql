-- 0106 (B51 판정 후속): SQLite 0106_month_close_lines_entity_key.sql 의 짝이다. 월말 스냅샷 줄의 유일 제약에 법인을 넣는다(추가 전용 트리거·행은 그대로).
ALTER TABLE month_close_lines DROP CONSTRAINT IF EXISTS month_close_lines_month_version_item_id_warehouse_id_key;
ALTER TABLE month_close_lines ADD CONSTRAINT month_close_lines_entity_month_version_item_wh_key UNIQUE (entity_id, month, version, item_id, warehouse_id);
