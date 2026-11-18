// 서버의 JSON 오류 본문({"error": "..."})에서 사람이 읽을 문구와 본문을 꺼낸다.
// ApiError.message 는 응답 본문 그대로라서(호출처가 필요하면 JSON 으로 읽는다) 문구만 보일 때는 이 함수를 거친다.
import { ApiError } from "./client";
import { businessMessage } from "../purchase/useLoad";

/** 오류가 ApiError 이고 본문이 JSON 객체면 (상태, 본문)을 돌려준다. */
export function serverJson<T = Record<string, unknown>>(e: unknown): { status: number; body: T } | null {
  if (!(e instanceof ApiError)) return null;
  try {
    const v = JSON.parse(e.message) as unknown;
    if (v && typeof v === "object") return { status: e.status, body: v as T };
  } catch {
    /* 일반 문구 */
  }
  return null;
}

/** 화면에 보일 오류 문구: JSON 본문의 error 를 꺼내 businessMessage 를 거친다. */
export function serverMessage(e: unknown): string {
  if (!(e instanceof ApiError)) return "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
  const j = serverJson<{ error?: string }>(e);
  const raw = j?.body.error ?? e.message;
  return raw ? businessMessage(raw) : "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
}
