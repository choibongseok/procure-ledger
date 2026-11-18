import { createHmac, timingSafeEqual } from 'node:crypto';

/**
 * 서명 규칙은 서버(internal/webhooks.Sign)와 같다.
 *   X-PL-Signature: v1=<hex HMAC-SHA256(비밀, 시각 + "." + 본문 바이트)>
 * 비밀을 교체한 뒤 24시간은 서명이 둘이다: "v1=<새>,v1=<옛>".
 */
export function sign(secret: string, timestamp: string, body: Buffer): string {
  return createHmac('sha256', secret).update(`${timestamp}.`).update(body).digest('hex');
}

export type VerifyFailure = 'missing_header' | 'bad_timestamp' | 'expired' | 'mismatch';
export type VerifyResult = { ok: true } | { ok: false; reason: VerifyFailure };

export interface VerifyInput {
  /** 허용하는 비밀들(현재 비밀, 교체 기간이면 옛 비밀도). */
  secrets: string[];
  timestamp: string | undefined;
  signatureHeader: string | undefined;
  /** 서버가 보낸 그대로의 본문 바이트. JSON 으로 풀었다 다시 만든 값이면 안 된다. */
  body: Buffer;
  toleranceSeconds: number;
  /** 시험에서 시계를 고정하려고 둔다. 현재 시각(초 단위 Unix 시각). */
  nowSeconds?: number;
}

export function verifySignature(input: VerifyInput): VerifyResult {
  const { secrets, timestamp, signatureHeader, body, toleranceSeconds } = input;
  if (!timestamp || !signatureHeader) return { ok: false, reason: 'missing_header' };
  if (!/^\d{1,12}$/.test(timestamp)) return { ok: false, reason: 'bad_timestamp' };

  const now = input.nowSeconds ?? Math.floor(Date.now() / 1000);
  if (Math.abs(now - Number(timestamp)) > toleranceSeconds) return { ok: false, reason: 'expired' };

  const given = signatureHeader
    .split(',')
    .map((p) => p.trim())
    .filter((p) => p.startsWith('v1='))
    .map((p) => Buffer.from(p.slice(3), 'utf8'));

  // 어느 쪽이 맞았는지 시간으로 새지 않도록 모든 조합을 끝까지 비교한다.
  let matched = false;
  for (const secret of secrets) {
    const want = Buffer.from(sign(secret, timestamp, body), 'utf8');
    for (const got of given) {
      if (got.length === want.length && timingSafeEqual(got, want)) matched = true;
    }
  }
  return matched ? { ok: true } : { ok: false, reason: 'mismatch' };
}
