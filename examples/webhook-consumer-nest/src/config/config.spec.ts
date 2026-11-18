import { ConfigError, loadConfig } from './config';

const ok = { PL_API_BASE_URL: 'http://127.0.0.1:8080/', PL_API_TOKEN: 'pli_x', PL_WEBHOOK_SECRET: 'whsec_a' };

describe('설정 검증', () => {
  it('필수 값만 있으면 기본값으로 채워진다', () => {
    expect(loadConfig(ok)).toEqual({
      port: 3000,
      apiBaseUrl: 'http://127.0.0.1:8080',
      apiToken: 'pli_x',
      secrets: ['whsec_a'],
      toleranceSeconds: 300,
      store: 'memory',
      databaseUrl: undefined,
      initialCursor: 'head',
    });
  });

  it('교체 기간의 옛 비밀은 현재 비밀 뒤에 붙는다', () => {
    expect(loadConfig({ ...ok, PL_WEBHOOK_SECRET_PREVIOUS: 'whsec_old' }).secrets).toEqual(['whsec_a', 'whsec_old']);
  });

  it('문제를 하나씩이 아니라 모아서 알린다', () => {
    try {
      loadConfig({ PL_API_BASE_URL: 'ftp://x', PORT: '0', PL_STORE: 'redis' });
      throw new Error('던져지지 않음');
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigError);
      const problems = (err as ConfigError).problems.join('|');
      expect(problems).toContain('PORT');
      expect(problems).toContain('PL_API_BASE_URL');
      expect(problems).toContain('PL_API_TOKEN');
      expect(problems).toContain('PL_WEBHOOK_SECRET');
      expect(problems).toContain('PL_STORE');
    }
  });

  it('postgres 저장소는 DATABASE_URL 이 있어야 한다', () => {
    expect(() => loadConfig({ ...ok, PL_STORE: 'postgres' })).toThrow(/DATABASE_URL/);
    expect(loadConfig({ ...ok, PL_STORE: 'postgres', DATABASE_URL: 'postgres://u@h/db' }).store).toBe('postgres');
  });

  it('허용 오차 범위를 벗어난 값은 거부한다', () => {
    expect(() => loadConfig({ ...ok, PL_WEBHOOK_TOLERANCE_SECONDS: '0' })).toThrow(/PL_WEBHOOK_TOLERANCE_SECONDS/);
    expect(loadConfig({ ...ok, PL_WEBHOOK_TOLERANCE_SECONDS: '60' }).toleranceSeconds).toBe(60);
  });
});
