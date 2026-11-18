import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { roleName, useRoleOptions } from "./roleNames";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});

describe("역할 선택지와 이름", () => {
  it("사용자 정의 역할(r_번호)을 선택지에 넣고 이름으로 보인다", async () => {
    vi.mocked(api.get).mockResolvedValue({
      roles: [
        { key: "staff", name: "직원" },
        { key: "r_7", name: "구매 보조" },
      ],
    });
    const { result } = renderHook(() => useRoleOptions());
    expect(result.current.options.length).toBe(5); // 받기 전에는 기본 역할 5개
    await waitFor(() => expect(result.current.options.map((o) => o.key)).toEqual(["staff", "r_7"]));
    expect(roleName("r_7")).toBe("구매 보조");
    expect(roleName("r_99")).toBe("(삭제된 역할)");
    expect(roleName("")).toBe("전체");
  });

  it("목록을 받지 못하면 기본 역할 5개로 대신한다", async () => {
    vi.mocked(api.get).mockRejectedValue(new Error("403"));
    const { result } = renderHook(() => useRoleOptions());
    await waitFor(() => expect(result.current.options.map((o) => o.name)).toContain("재무"));
  });
});
