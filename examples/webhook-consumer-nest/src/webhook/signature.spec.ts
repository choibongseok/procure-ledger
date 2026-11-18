import { sign, verifySignature } from './signature';

const NOW = 1_700_000_000;
const body = Buffer.from('{"id":7,"type":"vendor.updated"}');
const base = { secrets: ['whsec_new'], body, toleranceSeconds: 300, nowSeconds: NOW };
const ts = String(NOW);
const header = (...secrets: string[]) => secrets.map((s) => `v1=${sign(s, ts, body)}`).join(',');

describe('서명 검증', () => {
  it('서버 구현과 같은 값이다(고정 벡터)', () => {
    // 같은 입력을 openssl 로 계산한 값: printf '%s.%s' 1700000000 '{"id":7,"type":"vendor.updated"}' | openssl dgst -sha256 -hmac whsec_new -hex
    expect(sign('whsec_new', ts, body)).toBe('42771ad056b9820ea0b3287eb775f486f93d5b5fd6d0f24bc09e889d7d99982e');
  });

  it('맞는 서명은 통과한다', () => {
    expect(verifySignature({ ...base, timestamp: ts, signatureHeader: header('whsec_new') })).toEqual({ ok: true });
  });

  it('본문이 한 바이트라도 바뀌면 거부한다', () => {
    const tampered = Buffer.from('{"id":8,"type":"vendor.updated"}');
    expect(verifySignature({ ...base, body: tampered, timestamp: ts, signatureHeader: header('whsec_new') })).toEqual({ ok: false, reason: 'mismatch' });
  });

  it('다른 비밀로 만든 서명은 거부한다', () => {
    expect(verifySignature({ ...base, timestamp: ts, signatureHeader: header('whsec_other') })).toEqual({ ok: false, reason: 'mismatch' });
  });

  it('길이가 다른 서명 값도 예외 없이 거부한다', () => {
    expect(verifySignature({ ...base, timestamp: ts, signatureHeader: 'v1=abcd' })).toEqual({ ok: false, reason: 'mismatch' });
  });

  it('v1 이 아닌 서명만 있으면 거부한다', () => {
    expect(verifySignature({ ...base, timestamp: ts, signatureHeader: `v2=${sign('whsec_new', ts, body)}` })).toEqual({ ok: false, reason: 'mismatch' });
  });

  it('헤더가 없으면 거부한다', () => {
    expect(verifySignature({ ...base, timestamp: undefined, signatureHeader: header('whsec_new') })).toEqual({ ok: false, reason: 'missing_header' });
    expect(verifySignature({ ...base, timestamp: ts, signatureHeader: undefined })).toEqual({ ok: false, reason: 'missing_header' });
  });

  it('시각이 숫자가 아니면 거부한다', () => {
    expect(verifySignature({ ...base, timestamp: '17e8', signatureHeader: header('whsec_new') })).toEqual({ ok: false, reason: 'bad_timestamp' });
  });

  it('허용 오차 안(5분 경계)은 통과하고 넘으면 만료로 거부한다', () => {
    const at = (t: number) => {
      const s = String(t);
      return verifySignature({ ...base, timestamp: s, signatureHeader: `v1=${sign('whsec_new', s, body)}` });
    };
    expect(at(NOW - 300)).toEqual({ ok: true });
    expect(at(NOW + 300)).toEqual({ ok: true });
    expect(at(NOW - 301)).toEqual({ ok: false, reason: 'expired' });
    expect(at(NOW + 301)).toEqual({ ok: false, reason: 'expired' });
  });

  it('만료된 시각은 서명이 맞아도 거부한다(리플레이 방지)', () => {
    const old = String(NOW - 3600);
    expect(verifySignature({ ...base, timestamp: old, signatureHeader: `v1=${sign('whsec_new', old, body)}` })).toEqual({ ok: false, reason: 'expired' });
  });

  describe('비밀 교체 기간', () => {
    it('서버가 새·옛 서명을 함께 보내면 새 비밀만 가진 수신기도 통과한다', () => {
      expect(verifySignature({ ...base, secrets: ['whsec_new'], timestamp: ts, signatureHeader: header('whsec_new', 'whsec_old') })).toEqual({ ok: true });
    });

    it('옛 비밀만 가진 수신기도 같은 헤더로 통과한다', () => {
      expect(verifySignature({ ...base, secrets: ['whsec_old'], timestamp: ts, signatureHeader: header('whsec_new', 'whsec_old') })).toEqual({ ok: true });
    });

    it('수신기가 두 비밀을 모두 허용하면 어느 쪽 서명 하나만 와도 통과한다', () => {
      const both = { ...base, secrets: ['whsec_new', 'whsec_old'], timestamp: ts };
      expect(verifySignature({ ...both, signatureHeader: header('whsec_old') })).toEqual({ ok: true });
      expect(verifySignature({ ...both, signatureHeader: header('whsec_new') })).toEqual({ ok: true });
    });

    it('두 비밀 어느 쪽도 아니면 거부한다', () => {
      expect(verifySignature({ ...base, secrets: ['whsec_new', 'whsec_old'], timestamp: ts, signatureHeader: header('whsec_x', 'whsec_y') })).toEqual({ ok: false, reason: 'mismatch' });
    });
  });
});
