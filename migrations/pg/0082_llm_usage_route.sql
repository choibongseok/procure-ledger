-- 0082 (B45 2단계): SQLite 0082_llm_usage_route.sql 의 짝이다.
ALTER TABLE llm_usage ADD COLUMN route_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE llm_usage ADD COLUMN attempts BIGINT NOT NULL DEFAULT 1;
ALTER TABLE llm_usage ADD COLUMN fallback_from TEXT NOT NULL DEFAULT '';
ALTER TABLE llm_usage ADD COLUMN fallback_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE llm_usage ADD COLUMN latency_ms BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_llm_usage_provider_at ON llm_usage(provider, at);
