-- 0024: 거래처 평가(B25 ④, 6절) 계산용 인덱스. 평가는 조회 때 SQL 집계로 계산하고 저장하지 않는다.
CREATE INDEX idx_receipts_po_at ON receipts(po_id, received_at);
CREATE INDEX idx_verdicts_po_confirmed ON document_verdicts(po_id, confirmed_at);
