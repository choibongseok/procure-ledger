import { FeedChange } from '../store/types';
import { WebhookPayload } from '../webhook/payload';

/** 피드 한 줄에 대응하는 웹훅 본문(서버가 보내는 모양). */
export function payloadFor(line: FeedChange): WebhookPayload {
  return {
    id: line.seq,
    type: `${line.resource}.${line.op}`,
    resource: line.resource,
    object_id: line.id,
    op: line.op,
    status: line.status,
    occurred_at: line.at,
    api_url: `http://pl.test/api/v1/${line.resource}?id=${line.id}`,
  };
}
