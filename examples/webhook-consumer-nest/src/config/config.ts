export const APP_CONFIG = Symbol('APP_CONFIG');

export interface AppConfig {
  port: number;
  /** ProcureLedger 서버 주소(끝의 / 없음). */
  apiBaseUrl: string;
  /** 연동 토큰(changes:read). */
  apiToken: string;
  /** 서명 검증에 쓰는 비밀. 첫 값이 현재 비밀이고, 교체 기간에는 옛 비밀이 뒤에 붙는다. */
  secrets: string[];
  toleranceSeconds: number;
  store: 'memory' | 'postgres';
  databaseUrl?: string;
  initialCursor: 'head' | 'start';
}

export class ConfigError extends Error {
  constructor(readonly problems: string[]) {
    super(`설정이 올바르지 않습니다: ${problems.join('; ')}`);
    this.name = 'ConfigError';
  }
}

type Env = Record<string, string | undefined>;

function int(env: Env, name: string, fallback: number, min: number, max: number, problems: string[]): number {
  const raw = env[name];
  if (raw === undefined || raw === '') return fallback;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < min || n > max) {
    problems.push(`${name} 는 ${min}~${max} 사이의 정수여야 합니다`);
    return fallback;
  }
  return n;
}

/** 환경변수를 읽어 검증한다. 문제는 하나씩 던지지 않고 모아서 한 번에 알린다. */
export function loadConfig(env: Env = process.env): AppConfig {
  const problems: string[] = [];

  const port = int(env, 'PORT', 3000, 1, 65535, problems);
  const toleranceSeconds = int(env, 'PL_WEBHOOK_TOLERANCE_SECONDS', 300, 1, 3600, problems);

  let apiBaseUrl = (env.PL_API_BASE_URL ?? '').trim().replace(/\/+$/, '');
  try {
    const u = new URL(apiBaseUrl);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error('scheme');
  } catch {
    problems.push('PL_API_BASE_URL 은 http(s) 주소여야 합니다');
    apiBaseUrl = '';
  }

  const apiToken = (env.PL_API_TOKEN ?? '').trim();
  if (!apiToken) problems.push('PL_API_TOKEN 이 필요합니다(changes:read 범위의 연동 토큰)');

  const current = (env.PL_WEBHOOK_SECRET ?? '').trim();
  if (!current) problems.push('PL_WEBHOOK_SECRET 이 필요합니다');
  const previous = (env.PL_WEBHOOK_SECRET_PREVIOUS ?? '').trim();
  const secrets = [current, previous].filter((s) => s !== '');

  const storeRaw = (env.PL_STORE ?? 'memory').trim();
  if (storeRaw !== 'memory' && storeRaw !== 'postgres') problems.push('PL_STORE 는 memory 또는 postgres 여야 합니다');
  const store = storeRaw === 'postgres' ? 'postgres' : 'memory';
  const databaseUrl = (env.DATABASE_URL ?? '').trim() || undefined;
  if (store === 'postgres' && !databaseUrl) problems.push('PL_STORE=postgres 이면 DATABASE_URL 이 필요합니다');

  const cursorRaw = (env.PL_INITIAL_CURSOR ?? 'head').trim();
  if (cursorRaw !== 'head' && cursorRaw !== 'start') problems.push('PL_INITIAL_CURSOR 는 head 또는 start 여야 합니다');
  const initialCursor = cursorRaw === 'start' ? 'start' : 'head';

  if (problems.length > 0) throw new ConfigError(problems);
  return { port, apiBaseUrl, apiToken, secrets, toleranceSeconds, store, databaseUrl, initialCursor };
}
