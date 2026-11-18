import {
  BadRequestException,
  Controller,
  Get,
  Headers,
  HttpCode,
  Inject,
  Logger,
  Post,
  RawBodyRequest,
  Req,
  ServiceUnavailableException,
  UnauthorizedException,
} from '@nestjs/common';
import type { Request } from 'express';
import { ChangesApiError } from '../changes-client/changes-client';
import { APP_CONFIG, AppConfig } from '../config/config';
import { EVENT_STORE, EventStore } from '../store/types';
import { PayloadError, parsePayload } from './payload';
import { verifySignature } from './signature';
import { FeedBehindError, WebhookService } from './webhook.service';

@Controller()
export class WebhookController {
  private readonly logger = new Logger(WebhookController.name);

  constructor(
    private readonly webhook: WebhookService,
    @Inject(APP_CONFIG) private readonly cfg: AppConfig,
    @Inject(EVENT_STORE) private readonly store: EventStore,
  ) {}

  /**
   * 응답 코드가 서버의 재시도 규칙을 정한다.
   *   2xx  성공(중복·낡은 이벤트도 200)  /  401·400  재시도하지 않고 포기  /  503  1분·5분·30분… 간격으로 다시 보냄
   */
  @Post('webhooks/procure-ledger')
  @HttpCode(200)
  async receive(
    @Req() req: RawBodyRequest<Request>,
    @Headers('x-pl-timestamp') timestamp?: string,
    @Headers('x-pl-signature') signature?: string,
    @Headers('x-pl-event-id') eventIdHeader?: string,
    @Headers('x-pl-delivery') delivery?: string,
  ) {
    const raw = req.rawBody;
    if (!raw) throw new BadRequestException({ error: { code: 'empty_body', message: '본문이 없습니다' } });

    // 서명은 파싱 전에, 서버가 보낸 바이트 그대로에 대해 검증한다.
    const v = verifySignature({
      secrets: this.cfg.secrets,
      timestamp,
      signatureHeader: signature,
      body: raw,
      toleranceSeconds: this.cfg.toleranceSeconds,
    });
    if (!v.ok) {
      this.logger.warn(`서명 거부: ${v.reason}`);
      throw new UnauthorizedException({ error: { code: 'invalid_signature', message: '서명을 확인하지 못했습니다' } });
    }

    let payload;
    try {
      payload = parsePayload(raw);
    } catch (err) {
      if (err instanceof PayloadError) throw new BadRequestException({ error: { code: 'invalid_payload', message: err.message } });
      throw err;
    }
    if (eventIdHeader !== undefined && !payload.test && eventIdHeader !== String(payload.id)) {
      throw new BadRequestException({ error: { code: 'invalid_payload', message: 'X-PL-Event-Id 와 본문 id 가 다릅니다' } });
    }

    try {
      return await this.webhook.handle(payload, delivery);
    } catch (err) {
      if (err instanceof FeedBehindError || err instanceof ChangesApiError) {
        this.logger.warn(`피드 재조회 실패, 서버가 다시 보내도록 503: ${(err as Error).message}`);
        throw new ServiceUnavailableException({ error: { code: 'feed_unavailable', message: '변경 피드를 읽지 못했습니다' } });
      }
      throw err;
    }
  }

  @Get('healthz')
  health() {
    return { ok: true };
  }

  /** 지금까지 맞춘 상태(시험·점검용). */
  @Get('state')
  async state() {
    return { cursor: (await this.store.getCursor()) ?? null, objects: await this.store.listObjects() };
  }
}
