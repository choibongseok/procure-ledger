import { describe, expect, it } from "vitest";
import { ApiError } from "./client";
import { serverJson, serverMessage } from "./serverError";

describe("서버 오류 문구", () => {
  it("JSON 본문의 error 를 꺼내 업무 문구로 보인다", () => {
    expect(serverMessage(new ApiError(400, JSON.stringify({ error: "입력값이 올바르지 않습니다: 코드를 확인하세요" })))).toBe("코드를 확인하세요");
    expect(serverMessage(new ApiError(409, "그냥 문구"))).toBe("그냥 문구");
    expect(serverMessage(new ApiError(500, ""))).toMatch(/잠시 후 다시/);
    expect(serverMessage(new Error("x"))).toMatch(/잠시 후 다시/);
  });

  it("serverJson 은 JSON 객체 본문만 돌려준다", () => {
    expect(serverJson(new ApiError(409, JSON.stringify({ gate: { status: "blocked" } })))).toEqual({ status: 409, body: { gate: { status: "blocked" } } });
    expect(serverJson(new ApiError(409, "문구"))).toBeNull();
    expect(serverJson(new ApiError(409, "123"))).toBeNull();
    expect(serverJson("x")).toBeNull();
  });
});
