// 로그인 계열 API 의 오류 본문({code, message, until?})을 화면 문구로 바꾼다.
import { ApiError } from "../api/client";

export interface AuthErrorBody {
  code: string;
  message: string;
  until?: string;
}

/** 서버 오류 본문을 풀어 쓴다. JSON 이 아니면(예: 감사 실패 등 일반 오류) 본문 전체를 message 로 본다. */
export function parseAuthError(e: unknown): AuthErrorBody {
  if (e instanceof ApiError) {
    try {
      const b = JSON.parse(e.message) as Partial<AuthErrorBody>;
      if (b && typeof b.message === "string") return { code: b.code ?? "", message: b.message, until: b.until };
    } catch {
      /* 본문이 JSON 이 아님 */
    }
    if (e.status === 429) return { code: "rate_limited", message: "요청이 너무 많습니다. 잠시 뒤 다시 시도해 주세요" };
    if (e.status >= 500) return { code: "server", message: "서버 오류가 발생했습니다. 잠시 뒤 다시 시도하고, 계속되면 관리자에게 알려 주세요." };
    return { code: "", message: e.message || "요청을 처리하지 못했습니다." };
  }
  return { code: "", message: "서버에 연결하지 못했습니다. 네트워크를 확인하고 다시 시도해 주세요." };
}
