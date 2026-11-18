import { Logger } from '@nestjs/common';
import { AppConfig } from '../config/config';
import { MemoryEventStore } from '../store/memory-event-store';
import { FakeFeed } from '../testing/fake-feed';
import { payloadFor } from '../testing/payload-helper';
import { FeedSyncService } from './feed-sync.service';
import { FeedBehindError, WebhookService } from './webhook.service';
import { ChangesApiError } from '../changes-client/changes-client';

const cfg = (initialCursor: 'head' | 'start' = 'start'): AppConfig => ({
  port: 0, apiBaseUrl: 'http://pl.test', apiToken: 't', secrets: ['s'], toleranceSeconds: 300, store: 'memory', initialCursor,
});

function setup(initialCursor: 'head' | 'start' = 'start') {
  const store = new MemoryEventStore();
  const feed = new FakeFeed();
  // 서비스는 ChangesClient 의 head·list 만 쓰므로 같은 모양의 가짜를 넣는다.
  const sync = new FeedSyncService(store, feed as never, cfg(initialCursor));
  const service = new WebhookService(store, sync);
  return { store, feed, service };
}

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'log').mockImplementation();
  jest.spyOn(Logger.prototype, 'warn').mockImplementation();
});
afterAll(() => jest.restoreAllMocks());

describe('알림 처리', () => {
  it('알림을 받으면 본문이 아니라 피드를 읽어 객체 상태를 맞춘다', async () => {
    const { store, feed, service } = setup();
    feed.add('vendor', 10, 'created');
    const l2 = feed.add('journal', 5, 'status_changed', 'approved');
    // 본문의 status 를 일부러 틀리게 보낸다: 상태는 피드 값이어야 한다.
    const out = await service.handle({ ...payloadFor(l2), status: 'forged' });
    expect(out).toEqual({ status: 'applied', id: 2, cursor: 2, read: 2 });
    expect(await store.getObject('journal', 5)).toMatchObject({ status: 'approved', lastSeq: 2 });
    expect(await store.getObject('vendor', 10)).toMatchObject({ op: 'created' });
    expect((await store.getEvent(2))?.status).toBe('applied');
  });

  it('같은 이벤트 번호가 다시 오면 아무것도 하지 않는다', async () => {
    const { feed, service } = setup();
    const l = feed.add('vendor', 1, 'created');
    await service.handle(payloadFor(l));
    const calls = feed.listCalls.length;
    expect(await service.handle(payloadFor(l))).toEqual({ status: 'duplicate', id: 1 });
    expect(feed.listCalls.length).toBe(calls);
  });

  it('순서가 뒤집혀 커서보다 오래된 이벤트가 늦게 오면 기록만 한다', async () => {
    const { store, feed, service } = setup();
    const l1 = feed.add('vendor', 1, 'created');
    feed.add('vendor', 1, 'updated');
    const l3 = feed.add('vendor', 1, 'status_changed', 'blocked');
    await service.handle(payloadFor(l3)); // 3 이 먼저 오면서 1~3 을 모두 읽는다
    const calls = feed.listCalls.length;
    expect(await service.handle(payloadFor(l1))).toEqual({ status: 'stale', id: 1, cursor: 3 });
    expect(feed.listCalls.length).toBe(calls); // 다시 읽지 않는다
    expect((await store.getEvent(1))?.status).toBe('stale');
    expect(await store.getObject('vendor', 1)).toMatchObject({ lastSeq: 3, status: 'blocked' }); // 낡은 알림이 상태를 되돌리지 않는다
  });

  it('빠진 알림은 다음 알림이 피드를 읽을 때 함께 메워진다', async () => {
    const { store, feed, service } = setup();
    const l1 = feed.add('vendor', 1, 'created');
    await service.handle(payloadFor(l1));
    feed.add('item', 2, 'created'); // 알림 2·3 은 오지 않았다
    feed.add('item', 2, 'updated');
    const l4 = feed.add('receipt', 9, 'created');
    const out = await service.handle(payloadFor(l4));
    expect(out).toMatchObject({ status: 'applied', cursor: 4, read: 3 });
    expect(await store.getObject('item', 2)).toMatchObject({ op: 'updated', lastSeq: 3 });
    expect(await store.getObject('receipt', 9)).toBeDefined();
  });

  it('피드를 여러 쪽에 걸쳐 끝까지 읽는다', async () => {
    const { store, feed, service } = setup();
    feed.pageSize = 2;
    for (let i = 1; i <= 5; i++) feed.add('vendor', i, 'created');
    const out = await service.handle(payloadFor(feed.lines[4]));
    expect(out).toMatchObject({ status: 'applied', cursor: 5, read: 5 });
    expect(feed.listCalls).toEqual([0, 2, 4]);
    expect((await store.listObjects()).length).toBe(5);
  });

  it('피드 조회가 실패하면 오류를 올리고, 같은 이벤트를 다시 받으면 처리한다', async () => {
    const { store, feed, service } = setup();
    const l = feed.add('vendor', 1, 'created');
    feed.failing = true;
    await expect(service.handle(payloadFor(l))).rejects.toBeInstanceOf(ChangesApiError);
    expect((await store.getEvent(1))?.status).toBe('received'); // 완료가 아니다

    feed.failing = false; // 서버의 재시도
    expect((await service.handle(payloadFor(l))).status).toBe('applied');
    expect((await store.getEvent(1))?.status).toBe('applied');
  });

  it('피드가 이벤트 번호에 닿지 못하면 FeedBehindError 로 재시도를 유도한다', async () => {
    const { store, service } = setup();
    // 피드에는 아직 줄이 없는데 번호 5 알림이 왔다(복제 지연 등).
    await expect(service.handle({ id: 5, type: 'vendor.created', resource: 'vendor', object_id: 1, op: 'created', occurred_at: '', api_url: '' })).rejects.toBeInstanceOf(FeedBehindError);
    expect((await store.getEvent(5))?.status).toBe('received');
  });

  it('커서가 만료되면 현재 끝으로 다시 잡고 처리 완료로 본다', async () => {
    const { store, feed, service } = setup();
    feed.add('vendor', 1, 'created');
    feed.add('vendor', 1, 'updated');
    await store.setCursor(1);
    feed.expired = true;
    const out = await service.handle(payloadFor(feed.lines[1]));
    expect(out).toMatchObject({ status: 'applied', cursor: 2 });
    expect(await store.getCursor()).toBe(2);
  });

  it('저장된 커서가 없고 head 로 시작하면 이전 줄은 읽지 않는다', async () => {
    const { store, feed, service } = setup('head');
    feed.add('vendor', 1, 'created');
    feed.add('vendor', 2, 'created');
    const l3 = feed.add('vendor', 3, 'created');
    const out = await service.handle(payloadFor(l3));
    expect(out).toMatchObject({ status: 'applied', read: 0, cursor: 3 });
    expect(await store.listObjects()).toEqual([]);
    const l4 = feed.add('vendor', 4, 'created');
    await service.handle(payloadFor(l4));
    expect((await store.listObjects()).map((o) => o.id)).toEqual([4]);
  });

  it('동시에 들어온 알림도 피드를 겹쳐 읽지 않고 모두 반영한다', async () => {
    const { store, feed, service } = setup();
    const lines = [feed.add('vendor', 1, 'created'), feed.add('vendor', 2, 'created'), feed.add('vendor', 3, 'created')];
    const outs = await Promise.all(lines.map((l) => service.handle(payloadFor(l))));
    expect(outs.every((o) => o.status === 'applied')).toBe(true);
    expect(outs.reduce((n, o) => n + (o.status === 'applied' ? o.read : 0), 0)).toBe(3); // 줄을 한 번씩만 읽었다
    expect((await store.listObjects()).length).toBe(3);
  });

  it('시험 알림(ping)은 저장소를 건드리지 않는다', async () => {
    const { store, feed, service } = setup();
    const out = await service.handle({ id: 0, type: 'ping', resource: '', object_id: 0, op: '', occurred_at: '', api_url: '', test: true });
    expect(out).toEqual({ status: 'ping' });
    expect(feed.listCalls).toEqual([]);
    expect(await store.getCursor()).toBeUndefined();
  });
});
