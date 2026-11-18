import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, apiDownload } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { AuditLogPage } from "./AuditLogPage";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return { ...real, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } };
});

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const download = apiDownload as unknown as ReturnType<typeof vi.fn>;

const record = {
  seq: 1,
  at: "2026-11-10T01:02:03Z",
  actorId: 8,
  actorName: "구매직원",
  actorRole: "staff",
  channel: "web",
  action: "purchase.submit",
  targetType: "purchase_request",
  targetId: "7",
  result: "success",
  diff: [],
  detail: "",
  prevHash: "a",
  hash: "b",
};

beforeEach(() => {
  get.mockReset();
  download.mockReset();
  download.mockResolvedValue("감사 로그.csv");
});

function renderPage() {
  return render(
    <AuthProvider initialUser={{ id: 3, name: "감사", role: "auditor", departmentId: 1 }}>
      <AuditLogPage />
    </AuthProvider>,
  );
}

describe("감사 로그 내보내기 단추", () => {
  it("현재 적용된 조건만 보내고 입력 중인 사용자 이름은 적용된 뒤에야 보낸다", async () => {
    get.mockResolvedValue({ records: [record], nextBeforeSeq: 0 });
    renderPage();
    await screen.findByText("구매직원 (직원)");
    fireEvent.change(screen.getByLabelText("결과"), { target: { value: "success" } });
    // 결과 조건으로 다시 불러오는 동안은 단추가 꺼져 있으므로 켜진 뒤에, 이름 입력과 클릭을 같은 틱에서 한다(기계가 느려도 디바운스 시간이
    // 지나기 전임을 보장). 이름 입력 직후(디바운스 전)에는 아직 적용되지 않았다.
    await waitFor(() => expect(screen.getByRole("button", { name: "CSV" })).toBeEnabled());
    fireEvent.change(screen.getByPlaceholderText("이름"), { target: { value: "구매" } });
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(1));
    const first = String(download.mock.calls[0][0]);
    expect(first).toContain("/api/audit-logs/export?format=csv");
    expect(first).toContain("result=success");
    expect(first).not.toContain("actorName");
    // 입력이 적용된 뒤에는 조건에 들어간다
    await waitFor(() => expect(get.mock.calls.some(([u]) => String(u).includes("actorName="))).toBe(true));
    await waitFor(() => expect(screen.getByRole("button", { name: "엑셀(XLSX)" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "엑셀(XLSX)" }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(2));
    const second = String(download.mock.calls[1][0]);
    expect(second).toContain("format=xlsx");
    expect(second).toContain("actorName=%EA%B5%AC%EB%A7%A4");
  });

  it("기록이 없으면 단추가 꺼진다", async () => {
    get.mockResolvedValue({ records: [], nextBeforeSeq: 0 });
    renderPage();
    await screen.findByText("내보낼 자료가 없습니다.");
    expect(screen.getByRole("button", { name: "CSV" })).toBeDisabled();
  });
});
