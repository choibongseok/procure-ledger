-- 0018: 문서 갱신 차수(B23 단계 2 검증 지적, AC-201).
-- 읽음 판정이 초 단위 시각(read_at < updated_at) 비교라 같은 초 안의 갱신이 읽음으로 남았다.
-- purchase_requests.rev 는 행이 갱신될 때마다 트리거가 1 올리는 카운터이고, document_reads.seen_rev 는 읽을 때의 rev 다.
-- 안 읽음 = 한 번도 열지 않았거나, 연 뒤에 시각이 더 늦은 갱신이 있거나, 읽은 rev 보다 지금 rev 가 큰 것.
ALTER TABLE purchase_requests ADD COLUMN rev INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER trg_pr_rev AFTER UPDATE ON purchase_requests
WHEN NEW.rev = OLD.rev
BEGIN
    UPDATE purchase_requests SET rev = rev + 1 WHERE id = NEW.id;
END;

ALTER TABLE document_reads ADD COLUMN seen_rev INTEGER NOT NULL DEFAULT 0;
