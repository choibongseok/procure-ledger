import { Inject, Module, OnApplicationShutdown } from '@nestjs/common';
import { APP_CONFIG, AppConfig } from '../config/config';
import { MemoryEventStore } from './memory-event-store';
import { PgEventStore } from './pg-event-store';
import { EVENT_STORE, EventStore } from './types';

@Module({
  providers: [
    {
      provide: EVENT_STORE,
      inject: [APP_CONFIG],
      useFactory: async (cfg: AppConfig): Promise<EventStore> => {
        if (cfg.store === 'postgres') {
          const pg = new PgEventStore(cfg.databaseUrl as string);
          await pg.init();
          return pg;
        }
        return new MemoryEventStore();
      },
    },
  ],
  exports: [EVENT_STORE],
})
export class StoreModule implements OnApplicationShutdown {
  constructor(@Inject(EVENT_STORE) private readonly store: EventStore) {}

  async onApplicationShutdown() {
    await this.store.close?.();
  }
}
