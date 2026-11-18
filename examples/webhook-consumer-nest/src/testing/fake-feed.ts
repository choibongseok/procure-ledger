import { ChangesApiError, CursorExpiredError, FeedPage } from '../changes-client/changes-client';
import { FeedChange } from '../store/types';

/** 시험용 변경 피드. ChangesClient 와 같은 모양(head·list)이고 호출 횟수를 센다. */
export class FakeFeed {
  lines: FeedChange[] = [];
  headCalls = 0;
  listCalls: number[] = [];
  /** true 면 list 가 5xx 오류를 낸다. */
  failing = false;
  /** true 면 list 가 410 을 낸다. */
  expired = false;
  /** 한 쪽에 돌려줄 최대 줄 수(쪽 나누기 시험용). */
  pageSize = 200;

  add(resource: string, id: number, op: string, status?: string): FeedChange {
    const line: FeedChange = { seq: this.lines.length + 1, at: new Date(1_700_000_000_000 + this.lines.length * 1000).toISOString(), resource, id, op, status };
    this.lines.push(line);
    return line;
  }

  get headSeq() {
    return this.lines.length;
  }

  async head(): Promise<number> {
    this.headCalls++;
    return this.headSeq;
  }

  async list(after: number): Promise<FeedPage> {
    this.listCalls.push(after);
    if (this.failing) throw new ChangesApiError(503, 'unavailable', '일시 오류');
    if (this.expired) throw new CursorExpiredError('커서가 만료되었습니다');
    const rest = this.lines.filter((l) => l.seq > after);
    const page = rest.slice(0, this.pageSize);
    const hasMore = rest.length > page.length;
    const nextCursor = hasMore ? page[page.length - 1].seq : Math.max(after, this.headSeq);
    return { changes: page, nextCursor, hasMore };
  }
}
