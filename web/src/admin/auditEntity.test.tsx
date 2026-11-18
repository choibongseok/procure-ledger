// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuditLogPage } from "./AuditLogPage";

vi.mock("../api/client", () => ({ api: { get: vi.fn(), post: vi.fn() }, ApiError: class extends Error {} }));

let entities: { id: number; code: string; name: string }[] = [];
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => ({ can: () => true, entities }) }));
vi.mock("../layout/ExportButtons", () => ({ ExportButtons: (p: { params: Record<string, string> }) => <div data-testid="export" data-params={JSON.stringify(p.params)} /> }));

describe("감사 로그 화면의 법인 필터(B44)", () => {
  beforeEach(() => {
    vi.mocked(api.get).mockResolvedValue({ records: [], nextBeforeSeq: 0 });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("법인이 하나뿐이면 법인 칸이 없다", async () => {
    entities = [{ id: 1, code: "MAIN", name: "본사" }];
    render(<AuditLogPage />);
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    expect(screen.queryByLabelText("법인")).toBeNull();
  });

  it("법인이 둘 이상이면 고른 법인 코드가 조회와 내보내기 조건에 실린다", async () => {
    entities = [
      { id: 1, code: "MAIN", name: "본사" },
      { id: 2, code: "ABC", name: "계열사" },
    ];
    render(<AuditLogPage />);
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    fireEvent.change(screen.getByLabelText("법인"), { target: { value: "ABC" } });
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.some((c) => String(c[0]).includes("entity=ABC"))).toBe(true));
    expect(JSON.parse(screen.getByTestId("export").getAttribute("data-params") ?? "{}").entity).toBe("ABC");
  });
});
