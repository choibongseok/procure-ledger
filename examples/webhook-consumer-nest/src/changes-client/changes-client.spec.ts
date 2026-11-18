import { ChangesApiError, ChangesClient, CursorExpiredError, decodeCursor, encodeCursor } from './changes-client';

const cfg = { port: 0, apiBaseUrl: 'http://pl.test', apiToken: 'pli_secret', secrets: ['s'], toleranceSeconds: 300, store: 'memory' as const, initialCursor: 'head' as const };
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('변경 피드 클라이언트', () => {
  let fetchMock: jest.SpyInstance;
  beforeEach(() => {
    fetchMock = jest.spyOn(globalThis, 'fetch');
  });
  afterEach(() => fetchMock.mockRestore());

  it('커서 형식 c1.<번호> 를 주고받는다', () => {
    expect(encodeCursor(42)).toBe('c1.42');
    expect(decodeCursor('c1.42')).toBe(42);
    expect(() => decodeCursor('x1.1')).toThrow(ChangesApiError);
  });

  it('head 는 현재 끝 번호를 돌려준다', async () => {
    fetchMock.mockResolvedValue(json(200, { data: { cursor: 'c1.9' } }));
    await expect(new ChangesClient(cfg).head()).resolves.toBe(9);
    expect(fetchMock.mock.calls[0][0]).toBe('http://pl.test/api/v1/changes/head');
  });

  it('list 는 토큰을 Authorization 헤더로만 보내고 줄을 풀어 준다', async () => {
    fetchMock.mockResolvedValue(
      json(200, { data: [{ seq: 5, seq_str: '5', at: '2026-11-18T00:00:00Z', resource: 'vendor', id: 3, op: 'updated' }, { seq: 6, at: 'x', resource: 'journal', id: 1, op: 'status_changed', status: 'approved' }], next_cursor: 'c1.6', has_more: true }),
    );
    const page = await new ChangesClient(cfg).list(4, 50);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://pl.test/api/v1/changes?cursor=c1.4&limit=50');
    expect(url).not.toContain('pli_secret');
    expect(init.headers.Authorization).toBe('Bearer pli_secret');
    expect(page.nextCursor).toBe(6);
    expect(page.hasMore).toBe(true);
    expect(page.changes[0]).toMatchObject({ seq: 5, resource: 'vendor', id: 3, op: 'updated', status: undefined });
    expect(page.changes[1].status).toBe('approved');
  });

  it('410 은 CursorExpiredError 로 구분한다', async () => {
    fetchMock.mockResolvedValue(json(410, { error: { code: 'cursor_expired', message: '만료', request_id: 'r' } }));
    await expect(new ChangesClient(cfg).list(1)).rejects.toBeInstanceOf(CursorExpiredError);
  });

  it('그 밖의 오류는 상태 코드와 오류 코드를 담은 ChangesApiError 가 된다', async () => {
    fetchMock.mockResolvedValue(json(429, { error: { code: 'rate_limited', message: '한도' } }));
    await expect(new ChangesClient(cfg).list(1)).rejects.toMatchObject({ status: 429, code: 'rate_limited' });
  });

  it('연결 실패도 ChangesApiError 로 감싼다', async () => {
    fetchMock.mockRejectedValue(new TypeError('fetch failed'));
    await expect(new ChangesClient(cfg).head()).rejects.toMatchObject({ code: 'network' });
  });

  it('응답 형식이 어긋나면 502 로 알린다', async () => {
    fetchMock.mockResolvedValue(json(200, { nope: true }));
    await expect(new ChangesClient(cfg).list(1)).rejects.toMatchObject({ status: 502, code: 'bad_response' });
  });
});
