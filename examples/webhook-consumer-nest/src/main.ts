import 'reflect-metadata';
import { Logger } from '@nestjs/common';
import { NestFactory } from '@nestjs/core';
import { AppModule } from './app.module';
import { APP_CONFIG, AppConfig } from './config/config';

async function bootstrap() {
  // rawBody: 서명은 서버가 보낸 바이트 그대로에 대해 검증해야 하므로 원본 본문을 보존한다.
  const app = await NestFactory.create(AppModule, { rawBody: true });
  app.enableShutdownHooks();
  const cfg = app.get<AppConfig>(APP_CONFIG);
  await app.listen(cfg.port);
  new Logger('Bootstrap').log(`수신기 기동: 포트 ${cfg.port}, 저장소 ${cfg.store}`);
}

bootstrap().catch((err) => {
  // 설정 오류는 스택 없이 한 줄로 알린다.
  console.error(err?.name === 'ConfigError' ? err.message : err);
  process.exit(1);
});
