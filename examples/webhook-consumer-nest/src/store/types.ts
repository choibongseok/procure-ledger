/** 받은 알림(웹훅) 한 건의 처리 기록. */
export type EventStatus = 'received' | 'applied' | 'stale';

export interface StoredEvent {
  /** 이벤트 번호(X-PL-Event-Id = 본문 id = 변경 피드 번호). 중복 제거 키. */
  seq: number;
  type: string;
  resource: string;
  objectId: number;
  deliveryId?: string;
  receivedAt: string;
  status: EventStatus;
}

/** 변경 피드의 한 줄(값 없음). */
export interface FeedChange {
  seq: number;
  at: string;
  resource: string;
  id: number;
  op: string;
  status?: string;
}

/** 피드로 맞춘 객체별 마지막 상태. */
export interface MirroredObject {
  resource: string;
  id: number;
  op: string;
  status?: string;
  lastSeq: number;
  updatedAt: string;
}

/**
 * 수신기가 쓰는 저장소 계약. 기본 구현은 메모리이고 PostgreSQL 구현은 선택이다.
 * applyFeedPage 는 객체 상태와 커서를 한 번에(원자적으로) 갱신해야 한다.
 */
export interface EventStore {
  getEvent(seq: number): Promise<StoredEvent | undefined>;
  /** 번호가 같은 기록은 덮어쓴다. */
  saveEvent(event: StoredEvent): Promise<void>;
  /** 피드를 어디까지 읽어 반영했는지(번호). 아직 없으면 undefined. */
  getCursor(): Promise<number | undefined>;
  setCursor(seq: number): Promise<void>;
  /** 객체 상태는 더 큰 번호의 줄만 덮어쓴다. 커서는 줄어들지 않는다. */
  applyFeedPage(changes: FeedChange[], cursor: number): Promise<void>;
  getObject(resource: string, id: number): Promise<MirroredObject | undefined>;
  listObjects(): Promise<MirroredObject[]>;
  close?(): Promise<void>;
}

export const EVENT_STORE = Symbol('EVENT_STORE');
