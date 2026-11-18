import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ReconcilePage } from "./ReconcilePage";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;
const auditor: CurrentUser = { id: 6, name: "감사", role: "auditor", departmentId: 1 };

const rows = (original: number | null, verdict: "match" | "diff" | "") => [
  { code: "stock_value", label: "재고 금액 합(원)", original, system: 220000, diff: original === null ? null : 220000 - original, verdict, verdictLabel: verdict === "match" ? "일치" : verdict === "diff" ? "차이" : "", notes: verdict === "diff" ? ["승인 전 기초 전표 1건이 있어 계정별 원장에는 아직 들어가지 않았습니다."] : [] },
];

beforeEach(() => {
  get.mockReset();
});

describe("기초 데이터 대조 화면", () => {
  it("원 시스템 수치를 넣으면 쿼리로 보내 차이 판정과 원인 안내를 보인다", async () => {
    get.mockImplementation(async (p: string) => (p.includes("stock_value=250000") ? { rows: rows(250000, "diff"), pendingOpeningCount: 1 } : { rows: rows(null, ""), pendingOpeningCount: 1 }));
    render(
      <AuthProvider initialUser={auditor}>
        <MemoryRouter>
          <ReconcilePage />
        </MemoryRouter>
      </AuthProvider>,
    );
    expect(await screen.findByText(/승인 전 기초 전표가 1건 있습니다/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("재고 금액 합(원)"), { target: { value: "250,000" } });
    fireEvent.click(screen.getByRole("button", { name: "대조하기" }));
    expect(await screen.findByText("차이")).toBeInTheDocument();
    expect(screen.getByText(/계정별 원장에는 아직 들어가지 않았습니다/)).toBeInTheDocument();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/api/imports/reconcile?stock_value=250000"));
  });

  it("숫자가 아니면 조회하지 않고 한국어 안내를 보인다", async () => {
    get.mockResolvedValue({ rows: rows(null, ""), pendingOpeningCount: 0 });
    render(
      <AuthProvider initialUser={auditor}>
        <MemoryRouter>
          <ReconcilePage />
        </MemoryRouter>
      </AuthProvider>,
    );
    await screen.findByRole("table", { name: "기초 데이터 대조" });
    fireEvent.change(screen.getByLabelText("품목 수"), { target: { value: "열두 개" } });
    fireEvent.click(screen.getByRole("button", { name: "대조하기" }));
    expect(await screen.findByText(/0 이상의 정수로 입력하세요/)).toBeInTheDocument();
  });
});
