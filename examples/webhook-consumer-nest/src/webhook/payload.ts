/** 웹훅 본문. 값(금액·이름)은 없고 자원·번호·동작·상태·API 주소뿐이다. */
export interface WebhookPayload {
  id: number;
  type: string;
  resource: string;
  object_id: number;
  op: string;
  status?: string;
  occurred_at: string;
  api_url: string;
  test?: boolean;
}

export class PayloadError extends Error {}

/** 서명을 통과한 뒤에만 부른다. 필요한 칸이 없거나 형이 다르면 PayloadError. */
export function parsePayload(raw: Buffer): WebhookPayload {
  let v: unknown;
  try {
    v = JSON.parse(raw.toString('utf8'));
  } catch {
    throw new PayloadError('본문이 JSON 이 아닙니다');
  }
  const o = v as Record<string, unknown>;
  if (o === null || typeof o !== 'object' || Array.isArray(o)) throw new PayloadError('본문이 객체가 아닙니다');
  const isSeq = (x: unknown): x is number => typeof x === 'number' && Number.isSafeInteger(x) && x >= 0;
  if (!isSeq(o.id)) throw new PayloadError('id 가 올바르지 않습니다');
  if (typeof o.type !== 'string' || o.type === '') throw new PayloadError('type 이 없습니다');
  if (o.test === true || o.type === 'ping') {
    return { id: o.id, type: 'ping', resource: '', object_id: 0, op: '', occurred_at: String(o.occurred_at ?? ''), api_url: String(o.api_url ?? ''), test: true };
  }
  if (typeof o.resource !== 'string' || o.resource === '') throw new PayloadError('resource 가 없습니다');
  if (!isSeq(o.object_id)) throw new PayloadError('object_id 가 올바르지 않습니다');
  if (typeof o.op !== 'string') throw new PayloadError('op 가 없습니다');
  return {
    id: o.id,
    type: o.type,
    resource: o.resource,
    object_id: o.object_id,
    op: o.op,
    status: typeof o.status === 'string' ? o.status : undefined,
    occurred_at: typeof o.occurred_at === 'string' ? o.occurred_at : '',
    api_url: typeof o.api_url === 'string' ? o.api_url : '',
  };
}
