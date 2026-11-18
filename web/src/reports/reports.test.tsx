import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ApprovalReportPage } from "./ApprovalReportPage";
import { PurchaseReportPage } from "./PurchaseReportPage";
import { formatMinutes } from "./reportUi";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), apiDownload: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const options = { departments: [{ id: 1, name: "구매팀" }], forms: [{ code: "PUR", name: "구매 품의서" }] };
const purchase = {
  rows: [
    { departmentId: 1, departmentName: "구매팀", month: "2026-11", budget: 500000, orders: 1, used: 200000, inProgress: 100000, balance: 200000, executionPct: 40, forecastPct: 60, badge: "ok" },
    { departmentId: 2, departmentName: "영업팀", month: "2026-11", budget: null, orders: 1, used: 300000, inProgress: 0, balance: null, executionPct: null, forecastPct: null, badge: "none" },
  ],
  totals: { orders: 2, used: 500000, inProgress: 100000, budget: 500000, budgetedUsed: 200000, executionPct: 40, unbudgetedUsed: 300000 },
};

function renderPage(el: React.ReactNode) {
  return render(
    <AuthProvider initialUser={{ id: 1, name: "재무", role: "finance", departmentId: 1 }}>
      <MemoryRouter>{el}</MemoryRouter>
    </AuthProvider>,
  );
}

beforeEach(() => {
  get.mockReset();
});

describe("보고서 화면", () => {
  it("시간 표기는 일·시간·분이다", () => {
    expect(formatMinutes(1640)).toBe("1일 3시간 20분");
    expect(formatMinutes(0)).toBe("0분");
    expect(formatMinutes(90)).toBe("1시간 30분");
  });

  it("구매 실적: 예산 없음 배지와 합계, 제외 문구가 보이고 열 이름에 영문 코드가 없다", async () => {
    get.mockImplementation(async (path: string) => (path.startsWith("/api/reports/options") ? options : purchase));
    renderPage(<PurchaseReportPage />);
    expect(await screen.findByText("예산 없음")).toBeInTheDocument();
    expect(screen.getByText("합계")).toBeInTheDocument();
    expect(screen.getByText(/예산이 등록되지 않은 부서·월의 사용액 300,000원은 집행률에서 제외했습니다/)).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "집행률" })).toBeInTheDocument();
    expect(screen.getAllByText("40.0%", { selector: ".pct-num" }).length).toBe(2);
    expect(screen.getByText("계산 기준")).toBeInTheDocument();
  });

  it("구매 실적: 기간 오류는 조회 전에 문구로 막고 빈 결과 문구를 보인다", async () => {
    get.mockImplementation(async (path: string) => (path.startsWith("/api/reports/options") ? options : { from: "2026-04", to: "2026-11", rows: [], totals: { ...purchase.totals, orders: 0 } }));
    renderPage(<PurchaseReportPage />);
    expect(await screen.findByText("이 기간에 집계할 구매가 없습니다. 기간이나 부서를 바꿔 다시 조회하세요.")).toBeInTheDocument();
    expect(screen.getByLabelText("시작 월")).toHaveValue("2026-04"); // 기본 시작은 서버가 정한 회계연도 시작 월이다(화면이 1월로 고정하지 않는다)
    fireEvent.change(screen.getByLabelText("종료 월"), { target: { value: "2020-01" } });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    expect(await screen.findByText("종료일은 시작일 이후여야 합니다.")).toBeInTheDocument();
  });

  it("결재 소요: 단계 통계·지연 상위(결재자 없음)와 실패 문구", async () => {
    const report = {
      stats: [
        { formCode: "PUR", formName: "구매 품의서", stepTitle: "팀장", count: 2, avgMinutes: 1640, medMinutes: 1640, maxMinutes: 4320, rejected: 1, objected: 0, formTotal: false },
        { formCode: "PUR", formName: "구매 품의서", stepTitle: "전체", count: 2, avgMinutes: 1640, medMinutes: 1640, maxMinutes: 4320, rejected: 0, objected: 0, formTotal: true },
      ],
      delayed: [{ docId: 7, docNo: "PR-000007", title: "모니터", formName: "구매 품의서", stepTitle: "팀장", approverName: "", noApprover: true, elapsedMinutes: 300, stepStart: "2026-11-10T00:00:00Z", link: "purchase" }],
      excluded: 1,
    };
    get.mockImplementation(async (path: string) => (path.startsWith("/api/reports/options") ? options : report));
    renderPage(<ApprovalReportPage />);
    expect(await screen.findByText("결재자 없음")).toBeInTheDocument();
    expect(screen.getAllByText("1일 3시간 20분").length).toBeGreaterThan(0);
    expect(screen.getByText("전체(상신~결재 완료)")).toBeInTheDocument();
    expect(screen.getByText(/집계에서 제외한 건 1건/)).toBeInTheDocument();
    expect(screen.getByText(/주말·공휴일을 빼지 않은 실제 경과 시간입니다/)).toBeInTheDocument();
    get.mockImplementation(async (path: string) => {
      if (path.startsWith("/api/reports/options")) return options;
      throw new ApiError(500, "boom");
    });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    fireEvent.change(screen.getByLabelText("시작일"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "조회" }));
    await waitFor(() => expect(screen.getByText("보고서를 불러오지 못했습니다. 기간을 확인하고 다시 조회하세요.")).toBeInTheDocument());
  });
});
