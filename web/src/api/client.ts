// 모든 화면은 fetch 를 직접 쓰지 않고 이 클라이언트를 쓴다.
// - 세션은 서버 측(HttpOnly 쿠키)이라 same-origin 으로 쿠키를 보낸다.
// - 상태 변경 요청(POST/PUT/PATCH/DELETE)에는 CSRF 토큰 헤더를 붙인다. 토큰은 GET /api/me 응답에서 받는다.

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

let csrfToken = "";
let onUnauthorized: (() => void) | undefined;

export function setCsrfToken(token: string): void {
  csrfToken = token;
}

/** 401 응답 시 호출될 콜백(로그인 화면 이동)을 등록한다. */
export function setUnauthorizedHandler(fn: (() => void) | undefined): void {
  onUnauthorized = fn;
}

const SAFE = new Set(["GET", "HEAD", "OPTIONS"]);

export async function apiFetch<T = unknown>(path: string, init: RequestInit = {}): Promise<T> {
  return (await request<T>(path, init)).data;
}

async function request<T>(path: string, init: RequestInit): Promise<{ data: T; res: Response }> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (!SAFE.has(method) && csrfToken) headers.set("X-CSRF-Token", csrfToken);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(path, { ...init, method, headers, credentials: "same-origin" });
  if (res.status === 401) onUnauthorized?.();
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new ApiError(res.status, text.trim() || res.statusText);
  }
  if (res.status === 204) return { data: undefined as T, res };
  const type = res.headers.get("Content-Type") ?? "";
  return { data: (await (type.includes("application/json") ? res.json() : res.text())) as T, res };
}

/**
 * 파일 내려받기(내보내기). 오류는 화면에 문구를 보일 수 있게 fetch 로 받아 Blob 으로 저장한다(링크 이동으로 받으면 오류가 파일 대신 빈 화면이 된다).
 * 저장 이름은 응답의 Content-Disposition filename* 을 따른다.
 */
export async function apiDownload(path: string): Promise<string> {
  const res = await fetch(path, { credentials: "same-origin" });
  if (res.status === 401) onUnauthorized?.();
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new ApiError(res.status, text.trim() || res.statusText);
  }
  const cd = res.headers.get("Content-Disposition") ?? "";
  const m = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  let name = "export";
  if (m) {
    try {
      name = decodeURIComponent(m[1]);
    } catch {
      name = m[1];
    }
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return name;
}

/** 쪽 나눈 목록. 본문은 배열 그대로이고 같은 조건의 전체 건수는 X-Total-Count 헤더로 온다(없으면 받은 개수). */
export async function apiGetPage<T>(path: string): Promise<{ items: T[]; total: number }> {
  const { data, res } = await request<T[]>(path, {});
  const items = data ?? [];
  const n = Number(res.headers.get("X-Total-Count"));
  return { items, total: res.headers.has("X-Total-Count") && Number.isFinite(n) ? n : items.length };
}

export const api = {
  get: <T>(path: string) => apiFetch<T>(path),
  getPage: <T>(path: string) => apiGetPage<T>(path),
  post: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, { method: "POST", body: body === undefined ? undefined : JSON.stringify(body) }),
  put: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, { method: "PUT", body: body === undefined ? undefined : JSON.stringify(body) }),
  del: <T>(path: string) => apiFetch<T>(path, { method: "DELETE" }),
};
