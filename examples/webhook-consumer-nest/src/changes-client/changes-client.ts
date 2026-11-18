import { Inject, Injectable } from '@nestjs/common';
import { APP_CONFIG, AppConfig } from '../config/config';
import { FeedChange } from '../store/types';

export class ChangesApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message);
    this.name = 'ChangesApiError';
  }
}

/** 410 cursor_expired: 보존 기간이 지나 커서가 가리키는 번호가 사라졌다. 전체를 다시 적재해야 한다. */
export class CursorExpiredError extends ChangesApiError {
  constructor(message: string) {
    super(410, 'cursor_expired', message);
    this.name = 'CursorExpiredError';
  }
}

export interface FeedPage {
  changes: FeedChange[];
  /** 다음에 읽을 커서(번호). */
  nextCursor: number;
  hasMore: boolean;
}

export const encodeCursor = (seq: number) => `c1.${seq}`;

export function decodeCursor(cursor: string): number {
  const m = /^c1\.(\d+)$/.exec(cursor);
  if (!m) throw new ChangesApiError(502, 'bad_cursor', `서버가 알 수 없는 형식의 커서를 돌려주었습니다: ${cursor}`);
  return Number(m[1]);
}

const REQUEST_TIMEOUT_MS = 10_000;

/** 변경 피드(GET /api/v1/changes) 클라이언트. 연동 토큰은 Authorization 헤더로만 보낸다. */
@Injectable()
export class ChangesClient {
  constructor(@Inject(APP_CONFIG) private readonly cfg: AppConfig) {}

  /** 현재 피드의 끝 번호. */
  async head(): Promise<number> {
    const body = (await this.get('/api/v1/changes/head')) as { data?: { cursor?: string } };
    if (typeof body?.data?.cursor !== 'string') throw new ChangesApiError(502, 'bad_response', '/changes/head 응답에 cursor 가 없습니다');
    return decodeCursor(body.data.cursor);
  }

  /** 커서(번호) 뒤의 변경을 한 쪽 읽는다. */
  async list(after: number, limit = 200): Promise<FeedPage> {
    const body = (await this.get(`/api/v1/changes?cursor=${encodeURIComponent(encodeCursor(after))}&limit=${limit}`)) as {
      data?: Array<Record<string, unknown>>;
      next_cursor?: string;
      has_more?: boolean;
    };
    if (!Array.isArray(body?.data) || typeof body.next_cursor !== 'string') {
      throw new ChangesApiError(502, 'bad_response', '/changes 응답 형식이 올바르지 않습니다');
    }
    const changes: FeedChange[] = body.data.map((r) => ({
      seq: Number(r.seq),
      at: String(r.at),
      resource: String(r.resource),
      id: Number(r.id),
      op: String(r.op),
      status: typeof r.status === 'string' && r.status !== '' ? r.status : undefined,
    }));
    return { changes, nextCursor: decodeCursor(body.next_cursor), hasMore: body.has_more === true };
  }

  private async get(path: string): Promise<unknown> {
    let res: Response;
    try {
      res = await fetch(this.cfg.apiBaseUrl + path, {
        headers: { Authorization: `Bearer ${this.cfg.apiToken}`, Accept: 'application/json' },
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch (err) {
      throw new ChangesApiError(0, 'network', `변경 피드에 연결하지 못했습니다: ${(err as Error).message}`);
    }
    if (res.ok) return res.json();

    let code = 'unknown';
    let message = `HTTP ${res.status}`;
    try {
      const e = (await res.json()) as { error?: { code?: string; message?: string } };
      code = e.error?.code ?? code;
      message = e.error?.message ?? message;
    } catch {
      /* 본문이 JSON 이 아니면 상태 코드만 쓴다 */
    }
    if (res.status === 410) throw new CursorExpiredError(message);
    throw new ChangesApiError(res.status, code, message);
  }
}
