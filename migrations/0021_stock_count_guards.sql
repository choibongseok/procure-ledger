-- 0021: 재고 실사 줄 보호 트리거(B24 단계 2 검증 지적). 0020 은 이미 적용된 파일이라 고치지 않고 여기서 더한다.
--  - 작성 중(draft)·반려(rejected)가 아닌 실사 표(결재 중·반영 완료·차이 없음·폐기)의 줄은 SQL 로도 지우거나 더할 수 없다.
--  - 작성 중·반려 표의 줄 삭제는 전산 수량 새로 고침(줄 다시 만들기)이 쓰므로 허용한다.
CREATE TRIGGER trg_stock_count_lines_no_delete BEFORE DELETE ON stock_count_lines
WHEN (SELECT status FROM stock_counts WHERE id = OLD.count_id) NOT IN ('draft','rejected')
BEGIN SELECT RAISE(ABORT, '결재 중이거나 끝난 실사 표의 줄은 지울 수 없습니다'); END;

CREATE TRIGGER trg_stock_count_lines_no_insert BEFORE INSERT ON stock_count_lines
WHEN (SELECT status FROM stock_counts WHERE id = NEW.count_id) NOT IN ('draft','rejected')
BEGIN SELECT RAISE(ABORT, '결재 중이거나 끝난 실사 표에는 줄을 더할 수 없습니다'); END;
