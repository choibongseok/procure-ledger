import { Module } from '@nestjs/common';
import { ChangesClient } from './changes-client';

@Module({ providers: [ChangesClient], exports: [ChangesClient] })
export class ChangesClientModule {}
