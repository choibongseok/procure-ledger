import { Module } from '@nestjs/common';
import { ChangesClientModule } from './changes-client/changes-client.module';
import { ConfigModule } from './config/config.module';
import { StoreModule } from './store/store.module';
import { WebhookModule } from './webhook/webhook.module';

@Module({ imports: [ConfigModule, StoreModule, ChangesClientModule, WebhookModule] })
export class AppModule {}
