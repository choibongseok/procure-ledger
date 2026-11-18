-- 0082 (B45 2단계, 명세 4절): llm_usage 에 경로·대체 표시 열을 더한다(표를 다시 만들지 않는다, status CHECK 는 그대로).
--  route_id 0 = 기본 경로, attempts = 실제 시도 수, fallback_from = 주 공급자 코드(대체가 아니면 ''), fallback_reason = timeout|rate_limited|http_5xx|network|circuit_open|auth|bad_request|images|''.
-- provider·model 열은 최종으로 응답한(또는 마지막으로 시도한) 공급자·모델이다.
ALTER TABLE llm_usage ADD COLUMN route_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_usage ADD COLUMN attempts INTEGER NOT NULL DEFAULT 1;
ALTER TABLE llm_usage ADD COLUMN fallback_from TEXT NOT NULL DEFAULT '';
ALTER TABLE llm_usage ADD COLUMN fallback_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE llm_usage ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_llm_usage_provider_at ON llm_usage(provider, at);
