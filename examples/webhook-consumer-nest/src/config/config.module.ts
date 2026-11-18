import { Global, Module } from '@nestjs/common';
import { APP_CONFIG, loadConfig } from './config';

/** 환경변수를 한 번 읽어 검증한 설정을 어디서나 주입할 수 있게 한다. 잘못된 설정은 기동 때 실패한다. */
@Global()
@Module({
  providers: [{ provide: APP_CONFIG, useFactory: () => loadConfig(process.env) }],
  exports: [APP_CONFIG],
})
export class ConfigModule {}
