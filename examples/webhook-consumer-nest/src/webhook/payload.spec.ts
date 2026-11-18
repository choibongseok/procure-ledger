import { PayloadError, parsePayload } from './payload';

const b = (v: unknown) => Buffer.from(JSON.stringify(v));
const good = { id: 7, type: 'vendor.updated', resource: 'vendor', object_id: 3, op: 'updated', occurred_at: '2026-11-18T00:00:00Z', api_url: 'http://pl.test/api/v1/vendors?id=3' };

describe('본문 해석', () => {
  it('서버가 보내는 모양을 읽는다', () => {
    expect(parsePayload(b({ ...good, status: 'approved' }))).toMatchObject({ id: 7, resource: 'vendor', object_id: 3, status: 'approved' });
  });

  it('JSON 이 아니거나 필수 칸이 없거나 형이 다르면 PayloadError', () => {
    expect(() => parsePayload(Buffer.from('not json'))).toThrow(PayloadError);
    expect(() => parsePayload(b([1]))).toThrow(PayloadError);
    expect(() => parsePayload(b({ ...good, id: '7' }))).toThrow(PayloadError);
    expect(() => parsePayload(b({ ...good, id: -1 }))).toThrow(PayloadError);
    expect(() => parsePayload(b({ ...good, resource: '' }))).toThrow(PayloadError);
    expect(() => parsePayload(b({ ...good, object_id: 1.5 }))).toThrow(PayloadError);
  });

  it('시험 알림은 type 이 ping 이거나 test 가 참인 본문이다', () => {
    expect(parsePayload(b({ id: 0, type: 'ping', occurred_at: '', api_url: 'http://pl.test/api/v1/changes/head', test: true })).test).toBe(true);
  });
});
