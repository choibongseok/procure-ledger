import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { PrivacyLogPage, type LogEntry } from "./PrivacyLogPage";
import { PrivacySettingsPage } from "./PrivacySettingsPage";

vi.mock("../api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
    }
  },
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

vi.mock("../auth/AuthProvider", () => ({
  useAuth: () => ({ can: () => true }),
}));

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const put = vi.mocked(api.put);

function entry(p: Partial<LogEntry> & { id: number }): LogEntry {
  return {
    at: "2026-11-13T01:00:00Z", actorId: 7, actorName: "열람 관리자", actorUnit: "재무팀", actorRole: "admin", channel: "web", category: "contact",
    screen: "조직도 사람 카드", isExport: false, fields: ["email", "phone"], subjectCount: 1, exposure: "raw", offHours: false, ...p,
  };
}

const items: LogEntry[] = [
  entry({ id: 3, screen: "내보내기 조직 구성원", isExport: true, subjectCount: 52, dataset: "org_people", offHours: true }),
  entry({ id: 2, category: "account", screen: "계좌 원문 보기", fields: ["account_no"], subjectCount: 1, actorName: "재무 담당" }),
  entry({ id: 1, channel: "mcp", exposure: "mcp_anomaly", screen: "MCP get_payables", category: "account", fields: ["account_no"] }),
];

function renderPage(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
});

describe("개인정보 조회 기록 화면", () => {
  it("열람자·범주·화면·필드·정보주체 수와 내보내기·업무시간 밖·AI 도구 표시를 보인다(값은 없다)", async () => {
    get.mockResolvedValue({ items, retentionDays: 730 });
    renderPage(<PrivacyLogPage />);
    const table = await screen.findByRole("table", { name: "조회 기록" });
    expect(within(table).getByText("내보내기 조직 구성원")).toBeInTheDocument();
    expect(within(table).getByText("내보내기")).toBeInTheDocument();
    expect(within(table).getByText("업무시간 밖")).toBeInTheDocument();
    expect(within(table).getByText("52명")).toBeInTheDocument();
    expect(within(table).getByText("AI 도구 원문 유출")).toBeInTheDocument();
    expect(within(table).getAllByText("계좌").length).toBeGreaterThan(0);
    expect(screen.getByText(/보관 기간 730일/)).toHaveTextContent("법무 확인");
    expect(screen.queryByText(/@|010-/)).toBeNull();
  });

  it("필터는 서버 조회 매개변수로 가고 종료일은 하루를 포함하도록 다음 날 시작을 보낸다", async () => {
    get.mockResolvedValue({ items: [], retentionDays: 730 });
    renderPage(<PrivacyLogPage />);
    await screen.findByText("조회 기록이 없습니다.");
    fireEvent.change(screen.getByLabelText("범주"), { target: { value: "account" } });
    fireEvent.change(screen.getByLabelText("정보주체 이름"), { target: { value: "홍길동" } });
    fireEvent.change(screen.getByLabelText("종료일"), { target: { value: "2026-11-13" } });
    fireEvent.click(screen.getByLabelText("내보내기만"));
    await waitFor(() => {
      const last = get.mock.calls[get.mock.calls.length - 1][0] as string;
      expect(last).toContain("category=account");
      expect(last).toContain("subject=");
      expect(last).toContain("export=1");
      expect(last).toContain("to=2026-11-13T15%3A00%3A00.000Z");
    });
  });

  it("정보주체 보기: 이름·부서를 보이고 퇴사자는 (퇴사), 식별하지 못한 인원은 안내한다", async () => {
    get.mockImplementation(async (path: string) => {
      if (path.includes("/subjects")) return { items: [{ type: "user", id: 9, name: "퇴사한 사람", unit: "영업팀", left: true }, { type: "user", id: 10, name: "재직자", unit: "구매팀", left: false }], total: 2 };
      return { items: [entry({ id: 3, subjectCount: 5, isExport: true })], retentionDays: 730 };
    });
    renderPage(<PrivacyLogPage />);
    fireEvent.click(await screen.findByRole("button", { name: "정보주체 보기" }));
    const list = await screen.findByRole("list", { name: "정보주체" });
    expect(within(list).getByText(/퇴사한 사람/)).toHaveTextContent("(퇴사)");
    expect(within(list).getByText(/재직자/)).toHaveTextContent("구매팀");
    expect(screen.getByText(/이름을 식별하지 못한 3명/)).toBeInTheDocument();
  });

  it("무결성 확인 결과를 통과·실패로 구분해 보인다", async () => {
    get.mockResolvedValue({ items, retentionDays: 730 });
    renderPage(<PrivacyLogPage />);
    await screen.findByRole("table", { name: "조회 기록" });
    post.mockResolvedValueOnce({ ok: true, chainOk: true, checked: 12, digestsChecked: 3, digestOk: true, purged: false });
    fireEvent.click(screen.getByRole("button", { name: "무결성 확인" }));
    expect(await screen.findByText(/조회 기록 12건과 감사 체인의 하루 요약 3건을 확인했습니다/)).toBeInTheDocument();
    post.mockResolvedValueOnce({ ok: false, chainOk: false, checked: 4, brokenId: 5, why: "행 내용이 해시와 맞지 않습니다", digestsChecked: 0, digestOk: false, purged: false });
    fireEvent.click(screen.getByRole("button", { name: "무결성 확인" }));
    expect(await screen.findByText(/5번에서 어긋났습니다/)).toBeInTheDocument();
  });

  it("열람 통계 탭: 범주별·많이 본 사람·한 번에 많이 본 요청", async () => {
    get.mockImplementation(async (path: string) =>
      path.includes("/stats")
        ? { days: 30, total: 9, offHours: 2, exports: 1, anomalies: 0, byCategory: [{ category: "contact", requests: 5, subjects: 80 }], byActor: [{ actorId: 7, name: "열람 관리자", unit: "재무팀", requests: 5, subjects: 80 }], large: [entry({ id: 3, subjectCount: 52 })] }
        : { items: [], retentionDays: 730 },
    );
    renderPage(<PrivacyLogPage />);
    fireEvent.click(await screen.findByRole("tab", { name: "열람 통계" }));
    expect(await screen.findByLabelText("요약")).toHaveTextContent("전체 9건 · 내보내기 1건 · 업무시간 밖 2건");
    expect(screen.getByText(/연락처: 5건, 정보주체 80명/)).toBeInTheDocument();
    expect(screen.getAllByText(/열람 관리자/, { selector: "li" }).length).toBe(2);
  });
});

describe("조회 기록 보관 설정 화면", () => {
  const base = { retentionDays: 730, min: 365, max: 3650, defaultDays: 730, pending: [{ month: "2024-12", rows: 40 }] };

  it("현재 값과 파기 대상 달을 보이고, 늘리는 저장은 바로, 줄이는 저장은 확인을 거친다", async () => {
    get.mockResolvedValue(base);
    put.mockResolvedValue(base);
    renderPage(<PrivacySettingsPage />);
    const input = (await screen.findByLabelText("보관 기간(일)")) as HTMLInputElement;
    expect(input.value).toBe("730");
    expect(screen.getByText(/2024-12\(40건\)/)).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "1000" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/privacy/retention", { days: 1000 }));
    put.mockClear();
    fireEvent.change(input, { target: { value: "400" } });
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    expect(put).not.toHaveBeenCalled();
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "저장" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/api/admin/privacy/retention", { days: 400 }));
  });
});
