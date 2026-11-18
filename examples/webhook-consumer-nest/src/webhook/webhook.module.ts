import { Module } from '@nestjs/common';
import { ChangesClientModule } from '../changes-client/changes-client.module';
import { StoreModule } from '../store/store.module';
import { FeedSyncService } from './feed-sync.service';
import { WebhookController } from './webhook.controller';
import { WebhookService } from './webhook.service';

@Module({
  imports: [StoreModule, ChangesClientModule],
  controllers: [WebhookController],
  providers: [WebhookService, FeedSyncService],
})
export class WebhookModule {}
