import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, setCsrfToken, setUnauthorizedHandler } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(undefined);
  setCsrfToken("");
});

function stubFetch(res: Response) {
  const fn = vi.fn().mockImplementation(async () => res.clone());
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("api client", () => {
  it("상태 변경 요청에는 CSRF 헤더를, 조회에는 붙이지 않는다", async () => {
    const fn = stubFetch(new Response("{}", { headers: { "Content-Type": "application/json" } }));
    setCsrfToken("tok");
    await api.post("/api/x", { a: 1 });
    await api.get("/api/y");
    expect((fn.mock.calls[0][1].headers as Headers).get("X-CSRF-Token")).toBe("tok");
    expect((fn.mock.calls[1][1].headers as Headers).get("X-CSRF-Token")).toBeNull();
    expect(fn.mock.calls[0][1].credentials).toBe("same-origin");
  });
  it("401 이면 콜백을 부르고 ApiError 를 던진다", async () => {
    stubFetch(new Response("로그인 필요", { status: 401 }));
    const cb = vi.fn();
    setUnauthorizedHandler(cb);
    await expect(api.get("/api/me")).rejects.toMatchObject({ status: 401 });
    expect(cb).toHaveBeenCalled();
    expect(new ApiError(403, "x").status).toBe(403);
  });
});
