import { MemoryEventStore } from './memory-event-store';
import { PgEventStore } from './pg-event-store';
import { EventStore, StoredEvent } from './types';

const ev = (seq: number, status: StoredEvent['status'] = 'received'): StoredEvent => ({ seq, type: 'vendor.updated', resource: 'vendor', objectId: 1, receivedAt: '2026-11-18T00:00:00.000Z', status });
const line = (seq: number, id: number, op: string, status?: string) => ({ seq, at: '2026-11-18T00:00:00.000Z', resource: 'vendor', id, op, status });

// PostgreSQL 구현은 PL_TEST_PG_URL(빈 시험 DB)이 있을 때만 돈다. 없으면 건너뛴다.
const pgUrl = process.env.PL_TEST_PG_URL;
const stores: Array<[string, () => Promise<EventStore>, boolean]> = [
  ['메모리 저장소', async () => new MemoryEventStore(), true],
  [
    'PostgreSQL 저장소',
    async () => {
      const s = new PgEventStore(pgUrl as string);
      await s.init();
      await s.truncateAll();
      return s;
    },
    !!pgUrl,
  ],
];

describe.each(stores)('%s 계약', (_name, make, enabled) => {
  const t = enabled ? it : it.skip;
  let store: EventStore;
  beforeEach(async () => {
    if (enabled) store = await make();
  });
  afterEach(async () => {
    await store?.close?.();
  });

  t('이벤트 기록은 번호로 찾고 상태를 덮어쓸 수 있다', async () => {
    expect(await store.getEvent(7)).toBeUndefined();
    await store.saveEvent(ev(7));
    expect((await store.getEvent(7))?.status).toBe('received');
    await store.saveEvent(ev(7, 'applied'));
    expect(await store.getEvent(7)).toMatchObject({ seq: 7, status: 'applied', resource: 'vendor', objectId: 1 });
  });

  t('피드 쪽을 반영하면 객체 상태와 커서가 함께 갱신된다', async () => {
    expect(await store.getCursor()).toBeUndefined();
    await store.applyFeedPage([line(1, 10, 'created'), line(2, 10, 'updated', 'active'), line(3, 11, 'created')], 3);
    expect(await store.getObject('vendor', 10)).toMatchObject({ op: 'updated', status: 'active', lastSeq: 2 });
    expect((await store.listObjects()).map((o) => o.id)).toEqual([10, 11]);
    expect(await store.getCursor()).toBe(3);
  });

  t('더 오래된 줄은 객체 상태를 덮어쓰지 않고 커서는 줄어들지 않는다', async () => {
    await store.applyFeedPage([line(9, 10, 'status_changed', 'approved')], 9);
    await store.applyFeedPage([line(4, 10, 'created')], 4);
    expect(await store.getObject('vendor', 10)).toMatchObject({ op: 'status_changed', lastSeq: 9 });
    expect(await store.getCursor()).toBe(9);
  });
});
