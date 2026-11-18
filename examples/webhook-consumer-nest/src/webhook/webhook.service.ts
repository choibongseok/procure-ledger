import { Inject, Injectable, Logger } from '@nestjs/common';
import { EVENT_STORE, EventStore } from '../store/types';
import { FeedSyncService } from './feed-sync.service';
import { WebhookPayload } from './payload';

export type WebhookOutcome =
  | { status: 'ping' }
  /** 이미 처리한 이벤트 번호. 아무것도 하지 않는다. */
  | { status: 'duplicate'; id: number }
  /** 커서가 이미 이 번호를 지났다(순서 역전·늦은 도착). 기록만 남긴다. */
  | { status: 'stale'; id: number; cursor: number }
  /** 피드를 다시 읽어 상태를 맞췄다. */
  | { status: 'applied'; id: number; cursor: number; read: number };

/** 피드를 다시 읽었는데도 이벤트 번호에 닿지 못했다. 서버가 5xx 로 보고 다시 보내게 한다. */
export class FeedBehindError extends Error {}

@Injectable()
export class WebhookService {
  private readonly logger = new Logger(WebhookService.name);

  constructor(
    @Inject(EVENT_STORE) private readonly store: EventStore,
    private readonly feed: FeedSyncService,
  ) {}

  /**
   * 서명을 통과한 알림 하나를 처리한다.
   *
   * 1. 이미 처리한 번호면 무시한다(적어도 한 번 전달이라 중복이 온다).
   * 2. 번호를 먼저 기록하되 "처리 완료"는 상태를 맞춘 뒤에 표시한다. 피드 조회가 실패해
   *    5xx 를 돌려주면 서버가 다시 보내고, 그때는 완료가 아니므로 다시 처리한다.
   * 3. 커서가 이미 이 번호 이상이면 낡은 알림이다. 기록만 하고 끝낸다.
   * 4. 아니면 본문을 믿지 않고 피드를 커서부터 다시 읽는다. 빠진 알림도 이때 함께 메워진다.
   */
  async handle(p: WebhookPayload, deliveryId?: string): Promise<WebhookOutcome> {
    if (p.test || p.type === 'ping') return { status: 'ping' };

    const seen = await this.store.getEvent(p.id);
    if (seen && seen.status !== 'received') {
      this.logger.log(`중복 이벤트 무시 id=${p.id}`);
      return { status: 'duplicate', id: p.id };
    }
    if (!seen) {
      await this.store.saveEvent({
        seq: p.id,
        type: p.type,
        resource: p.resource,
        objectId: p.object_id,
        deliveryId,
        receivedAt: new Date().toISOString(),
        status: 'received',
      });
    }

    const cursor = await this.store.getCursor();
    if (cursor !== undefined && p.id <= cursor) {
      await this.mark(p, deliveryId, 'stale');
      this.logger.log(`낡은 이벤트 기록만 id=${p.id} cursor=${cursor}`);
      return { status: 'stale', id: p.id, cursor };
    }

    const res = await this.feed.sync();
    if (res.cursor < p.id && !res.expired) {
      throw new FeedBehindError(`피드 커서(${res.cursor})가 이벤트 번호(${p.id})에 닿지 못했습니다`);
    }
    await this.mark(p, deliveryId, 'applied');
    this.logger.log(`이벤트 반영 id=${p.id} 피드 ${res.read}줄 cursor=${res.cursor}`);
    return { status: 'applied', id: p.id, cursor: res.cursor, read: res.read };
  }

  private async mark(p: WebhookPayload, deliveryId: string | undefined, status: 'applied' | 'stale') {
    await this.store.saveEvent({
      seq: p.id,
      type: p.type,
      resource: p.resource,
      objectId: p.object_id,
      deliveryId,
      receivedAt: new Date().toISOString(),
      status,
    });
  }
}
