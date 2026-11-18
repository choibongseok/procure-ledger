import { EventStore, FeedChange, MirroredObject, StoredEvent } from './types';

export class MemoryEventStore implements EventStore {
  private readonly events = new Map<number, StoredEvent>();
  private readonly objects = new Map<string, MirroredObject>();
  private cursor: number | undefined;

  async getEvent(seq: number) {
    const e = this.events.get(seq);
    return e && { ...e };
  }

  async saveEvent(event: StoredEvent) {
    this.events.set(event.seq, { ...event });
  }

  async getCursor() {
    return this.cursor;
  }

  async setCursor(seq: number) {
    this.cursor = seq;
  }

  async applyFeedPage(changes: FeedChange[], cursor: number) {
    for (const c of changes) {
      const key = `${c.resource}:${c.id}`;
      const cur = this.objects.get(key);
      if (cur && cur.lastSeq >= c.seq) continue;
      this.objects.set(key, { resource: c.resource, id: c.id, op: c.op, status: c.status, lastSeq: c.seq, updatedAt: c.at });
    }
    this.cursor = Math.max(this.cursor ?? 0, cursor);
  }

  async getObject(resource: string, id: number) {
    const o = this.objects.get(`${resource}:${id}`);
    return o && { ...o };
  }

  async listObjects() {
    return [...this.objects.values()].sort((a, b) => a.lastSeq - b.lastSeq).map((o) => ({ ...o }));
  }
}
