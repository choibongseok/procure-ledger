import { INestApplication, Logger } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { AppModule } from '../src/app.module';
import { ChangesClient } from '../src/changes-client/changes-client';
import { APP_CONFIG, AppConfig } from '../src/config/config';
import { FakeFeed } from '../src/testing/fake-feed';
import { payloadFor } from '../src/testing/payload-helper';
import { sign } from '../src/webhook/signature';

const SECRET = 'whsec_current';
const OLD_SECRET = 'whsec_previous';

async function createApp(secrets: string[], feed: FakeFeed): Promise<INestApplication> {
  const config: AppConfig = { port: 0, apiBaseUrl: 'http://pl.test', apiToken: 'pli_t', secrets, toleranceSeconds: 300, store: 'memory', initialCursor: 'start' };
  const moduleRef = await Test.createTestingModule({ imports: [AppModule] })
    .overrideProvider(APP_CONFIG).useValue(config)
    .overrideProvider(ChangesClient).useValue(feed)
    .compile();
  const app = moduleRef.createNestApplication({ rawBody: true, logger: false });
  await app.init();
  return app;
}

interface SendOpts {
  secrets?: string[];
  timestamp?: number;
  headers?: Record<string, string | undefined>;
}

/** 서버(Go)와 같은 방식으로 서명해 보낸다. raw 는 문자열 그대로 보낸다(다시 직렬화하지 않는다). */
function post(app: INestApplication, raw: string, opts: SendOpts = {}) {
  const ts = String(opts.timestamp ?? Math.floor(Date.now() / 1000));
  const body = Buffer.from(raw);
  const sig = (opts.secrets ?? [SECRET]).map((s) => `v1=${sign(s, ts, body)}`).join(',');
  const eventId = /"id":\s*(\d+)/.exec(raw)?.[1];
  const h: Record<string, string | undefined> = { 'Content-Type': 'application/json', 'X-PL-Timestamp': ts, 'X-PL-Signature': sig, 'X-PL-Event-Id': eventId, 'X-PL-Delivery': '1', ...opts.headers };
  const r = request(app.getHttpServer()).post('/webhooks/procure-ledger');
  for (const [k, v] of Object.entries(h)) if (v !== undefined) r.set(k, v);
  return r.send(raw);
}

describe('POST /webhooks/procure-ledger', () => {
  let app: INestApplication;
  let feed: FakeFeed;

  beforeAll(() => {
    jest.spyOn(Logger.prototype, 'warn').mockImplementation();
  });
  beforeEach(async () => {
    feed = new FakeFeed();
    app = await createApp([SECRET], feed);
  });
  afterEach(() => app.close());
  afterAll(() => jest.restoreAllMocks());

  const state = async () => (await request(app.getHttpServer()).get('/state')).body;

  it('서명된 알림을 받으면 피드를 다시 읽어 상태를 맞춘다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)));
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ status: 'applied', id: 1, cursor: 1, read: 1 });
    expect(await state()).toMatchObject({ cursor: 1, objects: [{ resource: 'vendor', id: 3, op: 'updated', lastSeq: 1 }] });
  });

  it('원본 바이트로 검증한다: 공백·키 순서가 다른 본문도 서명이 맞으면 통과한다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const p = payloadFor(l);
    const raw = `{ "api_url" : "${p.api_url}",\n  "id":${p.id}, "type":"${p.type}", "resource":"${p.resource}", "object_id":${p.object_id}, "op":"${p.op}", "occurred_at":"${p.occurred_at}" }`;
    expect((await post(app, raw)).status).toBe(200);
  });

  it('서명이 틀리면 401 이고 상태는 바뀌지 않는다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)), { secrets: ['whsec_wrong'] });
    expect(res.status).toBe(401);
    expect(res.body.error.code).toBe('invalid_signature');
    expect(feed.listCalls).toEqual([]);
    expect((await state()).objects).toEqual([]);
  });

  it('본문을 바꾸면 서명이 어긋나 401 이다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const raw = JSON.stringify(payloadFor(l));
    const ts = String(Math.floor(Date.now() / 1000));
    const res = await request(app.getHttpServer()).post('/webhooks/procure-ledger')
      .set('Content-Type', 'application/json').set('X-PL-Timestamp', ts).set('X-PL-Signature', `v1=${sign(SECRET, ts, Buffer.from(raw))}`)
      .send(raw.replace('"object_id":3', '"object_id":4'));
    expect(res.status).toBe(401);
  });

  it('서명 헤더가 없으면 401 이다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)), { headers: { 'X-PL-Signature': undefined } });
    expect(res.status).toBe(401);
  });

  it('서명 시각이 5분을 벗어나면 서명이 맞아도 401 이다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)), { timestamp: Math.floor(Date.now() / 1000) - 601 });
    expect(res.status).toBe(401);
  });

  it('비밀 교체 기간: 서버가 새·옛 서명을 함께 보내면 새 비밀만 가진 수신기가 받는다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)), { secrets: [SECRET, OLD_SECRET] });
    expect(res.status).toBe(200);
  });

  it('비밀 교체 기간: 수신기에 새·옛 비밀을 모두 두면 옛 비밀 서명만 와도 받는다', async () => {
    await app.close();
    app = await createApp([SECRET, OLD_SECRET], feed);
    const l = feed.add('vendor', 3, 'updated');
    expect((await post(app, JSON.stringify(payloadFor(l)), { secrets: [OLD_SECRET] })).status).toBe(200);
  });

  it('같은 이벤트를 다시 받으면 200 으로 무시한다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const raw = JSON.stringify(payloadFor(l));
    expect((await post(app, raw)).body.status).toBe('applied');
    const again = await post(app, raw);
    expect(again.status).toBe(200);
    expect(again.body).toEqual({ status: 'duplicate', id: 1 });
    expect(feed.listCalls.length).toBe(1);
  });

  it('순서가 뒤집혀 늦게 온 낡은 이벤트는 200 으로 기록만 한다', async () => {
    const l1 = feed.add('vendor', 3, 'created');
    feed.add('vendor', 3, 'status_changed', 'blocked');
    const l3 = feed.add('vendor', 3, 'updated', 'blocked');
    expect((await post(app, JSON.stringify(payloadFor(l3)))).body.status).toBe('applied');
    const late = await post(app, JSON.stringify(payloadFor(l1)));
    expect(late.status).toBe(200);
    expect(late.body).toEqual({ status: 'stale', id: 1, cursor: 3 });
    expect((await state()).objects[0]).toMatchObject({ lastSeq: 3, op: 'updated' });
  });

  it('빠진 알림은 다음 알림이 와서 피드를 읽을 때 복구된다', async () => {
    feed.add('item', 1, 'created');
    feed.add('item', 2, 'created'); // 알림 1·2 가 유실됐다
    const l3 = feed.add('item', 3, 'created');
    await post(app, JSON.stringify(payloadFor(l3)));
    expect((await state()).objects.map((o: { id: number }) => o.id)).toEqual([1, 2, 3]);
  });

  it('피드를 읽지 못하면 503 을 돌려 서버가 다시 보내게 하고, 재전송은 처리된다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const raw = JSON.stringify(payloadFor(l));
    feed.failing = true;
    const first = await post(app, raw);
    expect(first.status).toBe(503);
    expect(first.body.error.code).toBe('feed_unavailable');

    feed.failing = false;
    const retry = await post(app, raw);
    expect(retry.status).toBe(200);
    expect(retry.body.status).toBe('applied');
    expect((await state()).objects.length).toBe(1);
  });

  it('서명은 맞지만 본문이 올바르지 않으면 400 이다', async () => {
    expect((await post(app, '{"id":"x"}', { headers: { 'X-PL-Event-Id': '1' } })).status).toBe(400);
    expect((await post(app, 'not json', { headers: { 'X-PL-Event-Id': '1' } })).status).toBe(400);
  });

  it('X-PL-Event-Id 와 본문 id 가 다르면 400 이다', async () => {
    const l = feed.add('vendor', 3, 'updated');
    const res = await post(app, JSON.stringify(payloadFor(l)), { headers: { 'X-PL-Event-Id': '99' } });
    expect(res.status).toBe(400);
  });

  it('시험 알림(ping)은 200 으로 받고 피드를 읽지 않는다', async () => {
    const raw = JSON.stringify({ id: 0, type: 'ping', occurred_at: '2026-11-18T00:00:00Z', api_url: 'http://pl.test/api/v1/changes/head', test: true });
    const res = await post(app, raw, { headers: { 'X-PL-Event-Id': undefined } });
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ status: 'ping' });
    expect(feed.listCalls).toEqual([]);
  });

  it('GET /healthz 는 200 이다', async () => {
    expect((await request(app.getHttpServer()).get('/healthz')).body).toEqual({ ok: true });
  });
});
