import { Pool } from 'pg';
import { EventStore, FeedChange, MirroredObject, StoredEvent } from './types';

/** PostgreSQL 구현(선택). 테이블은 처음 쓸 때 만든다. */
export class PgEventStore implements EventStore {
  private readonly pool: Pool;

  constructor(connectionString: string) {
    this.pool = new Pool({ connectionString });
  }

  async init(): Promise<void> {
    await this.pool.query(`
      CREATE TABLE IF NOT EXISTS pl_webhook_events (
        seq         BIGINT PRIMARY KEY,
        type        TEXT NOT NULL,
        resource    TEXT NOT NULL,
        object_id   BIGINT NOT NULL,
        delivery_id TEXT,
        received_at TIMESTAMPTZ NOT NULL,
        status      TEXT NOT NULL CHECK (status IN ('received', 'applied', 'stale'))
      );
      CREATE TABLE IF NOT EXISTS pl_feed_objects (
        resource   TEXT NOT NULL,
        id         BIGINT NOT NULL,
        op         TEXT NOT NULL,
        status     TEXT,
        last_seq   BIGINT NOT NULL,
        updated_at TIMESTAMPTZ NOT NULL,
        PRIMARY KEY (resource, id)
      );
      CREATE TABLE IF NOT EXISTS pl_feed_cursor (
        singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
        seq       BIGINT NOT NULL
      );`);
  }

  async getEvent(seq: number): Promise<StoredEvent | undefined> {
    const r = await this.pool.query('SELECT * FROM pl_webhook_events WHERE seq = $1', [seq]);
    const row = r.rows[0];
    return row && {
      seq: Number(row.seq),
      type: row.type,
      resource: row.resource,
      objectId: Number(row.object_id),
      deliveryId: row.delivery_id ?? undefined,
      receivedAt: new Date(row.received_at).toISOString(),
      status: row.status,
    };
  }

  async saveEvent(e: StoredEvent): Promise<void> {
    await this.pool.query(
      `INSERT INTO pl_webhook_events (seq, type, resource, object_id, delivery_id, received_at, status)
       VALUES ($1, $2, $3, $4, $5, $6, $7)
       ON CONFLICT (seq) DO UPDATE SET status = EXCLUDED.status`,
      [e.seq, e.type, e.resource, e.objectId, e.deliveryId ?? null, e.receivedAt, e.status],
    );
  }

  async getCursor(): Promise<number | undefined> {
    const r = await this.pool.query('SELECT seq FROM pl_feed_cursor');
    return r.rows[0] ? Number(r.rows[0].seq) : undefined;
  }

  async setCursor(seq: number): Promise<void> {
    await this.pool.query(
      `INSERT INTO pl_feed_cursor (singleton, seq) VALUES (TRUE, $1)
       ON CONFLICT (singleton) DO UPDATE SET seq = EXCLUDED.seq`,
      [seq],
    );
  }

  async applyFeedPage(changes: FeedChange[], cursor: number): Promise<void> {
    const client = await this.pool.connect();
    try {
      await client.query('BEGIN');
      for (const c of changes) {
        await client.query(
          `INSERT INTO pl_feed_objects (resource, id, op, status, last_seq, updated_at)
           VALUES ($1, $2, $3, $4, $5, $6)
           ON CONFLICT (resource, id) DO UPDATE
             SET op = EXCLUDED.op, status = EXCLUDED.status, last_seq = EXCLUDED.last_seq, updated_at = EXCLUDED.updated_at
             WHERE EXCLUDED.last_seq > pl_feed_objects.last_seq`,
          [c.resource, c.id, c.op, c.status ?? null, c.seq, c.at],
        );
      }
      await client.query(
        `INSERT INTO pl_feed_cursor (singleton, seq) VALUES (TRUE, $1)
         ON CONFLICT (singleton) DO UPDATE SET seq = GREATEST(pl_feed_cursor.seq, EXCLUDED.seq)`,
        [cursor],
      );
      await client.query('COMMIT');
    } catch (err) {
      await client.query('ROLLBACK');
      throw err;
    } finally {
      client.release();
    }
  }

  private toObject(row: Record<string, unknown>): MirroredObject {
    return {
      resource: row.resource as string,
      id: Number(row.id),
      op: row.op as string,
      status: (row.status as string | null) ?? undefined,
      lastSeq: Number(row.last_seq),
      updatedAt: new Date(row.updated_at as string).toISOString(),
    };
  }

  async getObject(resource: string, id: number): Promise<MirroredObject | undefined> {
    const r = await this.pool.query('SELECT * FROM pl_feed_objects WHERE resource = $1 AND id = $2', [resource, id]);
    return r.rows[0] && this.toObject(r.rows[0]);
  }

  async listObjects(): Promise<MirroredObject[]> {
    const r = await this.pool.query('SELECT * FROM pl_feed_objects ORDER BY last_seq');
    return r.rows.map((row) => this.toObject(row));
  }

  async close(): Promise<void> {
    await this.pool.end();
  }

  /** 시험에서 테이블을 비울 때 쓴다. */
  async truncateAll(): Promise<void> {
    await this.pool.query('TRUNCATE pl_webhook_events, pl_feed_objects, pl_feed_cursor');
  }
}
