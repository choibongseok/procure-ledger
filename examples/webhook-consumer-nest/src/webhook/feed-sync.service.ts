import { Inject, Injectable, Logger } from '@nestjs/common';
import { ChangesClient, CursorExpiredError } from '../changes-client/changes-client';
import { APP_CONFIG, AppConfig } from '../config/config';
import { EVENT_STORE, EventStore } from '../store/types';

export interface SyncResult {
  cursor: number;
  /** 이번에 읽은 피드 줄 수. */
  read: number;
  /** 커서가 만료되어 지금 위치로 다시 잡았다(그 사이의 변경은 목록 API 로 다시 적재해야 한다). */
  expired: boolean;
}

/** 한 번에 읽는 쪽 수의 상한(200건 x 100쪽). 넘으면 다음 호출이 이어서 읽는다. */
const MAX_PAGES = 100;

/**
 * 변경 피드를 커서로 다시 읽어 객체 상태를 맞춘다. 웹훅은 "무언가 바뀌었다"는 알림일 뿐이고
 * 상태의 정본은 피드다. 그래서 알림이 몇 건 빠지거나 순서가 뒤집혀도 다음 조회에서 따라잡는다.
 */
@Injectable()
export class FeedSyncService {
  private readonly logger = new Logger(FeedSyncService.name);
  private lock: Promise<unknown> = Promise.resolve();

  constructor(
    @Inject(EVENT_STORE) private readonly store: EventStore,
    private readonly changes: ChangesClient,
    @Inject(APP_CONFIG) private readonly cfg: AppConfig,
  ) {}

  /** 동시에 들어온 알림이 같은 구간을 두 번 읽지 않도록 한 번에 하나씩 돈다(프로세스 안에서). */
  sync(): Promise<SyncResult> {
    const run = this.lock.then(() => this.run());
    this.lock = run.catch(() => undefined);
    return run;
  }

  private async run(): Promise<SyncResult> {
    let cursor = await this.store.getCursor();
    if (cursor === undefined) {
      cursor = this.cfg.initialCursor === 'start' ? 0 : await this.changes.head();
      await this.store.setCursor(cursor);
      this.logger.log(`저장된 커서가 없어 ${this.cfg.initialCursor === 'start' ? '처음' : '현재 끝'}(${cursor})에서 시작합니다`);
    }

    let read = 0;
    try {
      for (let page = 0; page < MAX_PAGES; page++) {
        const res = await this.changes.list(cursor);
        await this.store.applyFeedPage(res.changes, res.nextCursor);
        read += res.changes.length;
        cursor = Math.max(cursor, res.nextCursor);
        if (!res.hasMore) break;
      }
    } catch (err) {
      if (!(err instanceof CursorExpiredError)) throw err;
      const head = await this.changes.head();
      await this.store.setCursor(head);
      this.logger.warn(`커서 ${cursor} 가 만료되어 ${head} 로 다시 잡았습니다. 그 사이 변경은 목록 API 로 다시 적재하세요`);
      return { cursor: head, read, expired: true };
    }
    return { cursor, read, expired: false };
  }
}
