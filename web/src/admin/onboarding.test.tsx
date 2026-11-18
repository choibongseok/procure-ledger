import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api/client";
import { BackupStatusPage } from "./BackupStatusPage";
import { adminHomeCards } from "./homeCards";
import { OnboardingPage, type Checklist, type ChecklistItem } from "./OnboardingPage";

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

const get = vi.mocked(api.get);
const post = vi.mocked(api.post);
const del = vi.mocked(api.del);

function item(p: Partial<ChecklistItem> & { id: string; title: string }): ChecklistItem {
  return { state: "todo", evidence: `${p.title} 근거`, link: `/x/${p.id}`, skippable: false, ...p };
}

const sample: Checklist = {
  items: [
    item({ id: "company", title: "회사 정보", state: "done" }),
    item({ id: "org", title: "조직도", state: "in_progress" }),
    item({ id: "opening_data", title: "기초 데이터", state: "todo", skippable: true }),
    item({ id: "notifications", title: "알림 설정", state: "not_applicable", skippable: true, skip: { reason: "메일을 쓰지 않음", by: 1, at: "2026-11-13T00:00:00Z" } }),
    item({ id: "keys", title: "키 설정", state: "unknown", evidence: "확인하지 못했습니다", link: "" }),
  ],
  summary: { total: 12, done: 1, notApplicable: 1 },
};

function renderIn(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  del.mockReset();
});

describe("도입 점검표 화면", () => {
  it("항목별 상태·근거·바로가기를 보이고 요약을 보인다(비밀·경로 없음)", async () => {
    get.mockResolvedValue(sample);
    renderIn(<OnboardingPage />);
    expect(await screen.findByRole("heading", { name: /1\. 회사 정보/ })).toBeInTheDocument();
    expect(screen.getByLabelText("진행 요약")).toHaveTextContent("완료 1 / 12");
    expect(screen.getByLabelText("진행 요약")).toHaveTextContent("해당 없음 1");
    const org = screen.getByRole("heading", { name: /2\. 조직도/ }).closest("li") as HTMLElement;
    expect(within(org).getByText("진행 중")).toBeInTheDocument();
    expect(within(org).getByRole("link", { name: "바로가기" })).toHaveAttribute("href", "/x/org");
    // 질의에 실패한 항목은 그 항목만 "확인하지 못했습니다"이고 링크가 없는 항목은 바로가기가 없다.
    const keys = screen.getByRole("heading", { name: /5\. 키 설정/ }).closest("li") as HTMLElement;
    expect(within(keys).getAllByText("확인하지 못했습니다").length).toBeGreaterThan(0);
    expect(within(keys).queryByRole("link")).toBeNull();
    expect(screen.getByText(/해당 없음 사유: 메일을 쓰지 않음/)).toBeInTheDocument();
  });

  it("해당 없음 표시는 사유 입력을 거쳐 서버로 가고, 끝나면 다시 불러온다", async () => {
    get.mockResolvedValue(sample);
    post.mockResolvedValue(undefined);
    renderIn(<OnboardingPage />);
    const row = (await screen.findByRole("heading", { name: /3\. 기초 데이터/ })).closest("li") as HTMLElement;
    expect(screen.queryByRole("heading", { name: /1\. 회사 정보/ })?.closest("li")?.querySelector("button")).toBeNull(); // 표시할 수 없는 항목에는 단추가 없다
    fireEvent.click(within(row).getByRole("button", { name: "해당 없음으로 표시" }));
    fireEvent.change(within(row).getByLabelText("해당 없음 사유"), { target: { value: "처음부터 새로 시작" } });
    fireEvent.click(within(row).getByRole("button", { name: "표시" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/api/admin/onboarding/skips", { item: "opening_data", reason: "처음부터 새로 시작" }));
    expect(await screen.findByText("해당 없음으로 표시했습니다.")).toBeInTheDocument();
    expect(get.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("서버가 거부하면 한국어 문구를 보이고, 해당 없음 풀기는 DELETE 로 간다", async () => {
    get.mockResolvedValue(sample);
    post.mockRejectedValue(new ApiError(422, "입력값이 올바르지 않습니다: 해당 없음으로 표시하는 사유를 입력하세요."));
    del.mockResolvedValue(undefined);
    renderIn(<OnboardingPage />);
    const row = (await screen.findByRole("heading", { name: /3\. 기초 데이터/ })).closest("li") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "해당 없음으로 표시" }));
    fireEvent.change(within(row).getByLabelText("해당 없음 사유"), { target: { value: " " } });
    fireEvent.click(within(row).getByRole("button", { name: "표시" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("해당 없음으로 표시하는 사유를 입력하세요.");
    const na = screen.getByRole("heading", { name: /4\. 알림 설정/ }).closest("li") as HTMLElement;
    fireEvent.click(within(na).getByRole("button", { name: "해당 없음 풀기" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/api/admin/onboarding/skips/notifications"));
  });

  it("목록을 불러오지 못하면 오류 문구, 항목이 없으면 빈 상태 문구를 보인다", async () => {
    get.mockRejectedValue(new ApiError(500, "서버 오류"));
    const { unmount } = renderIn(<OnboardingPage />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    unmount();
    get.mockResolvedValue({ items: [], summary: { total: 0, done: 0, notApplicable: 0 } });
    renderIn(<OnboardingPage />);
    expect(await screen.findByText("점검할 항목을 불러오지 못했습니다.")).toBeInTheDocument();
  });
});

describe("홈 요약 카드", () => {
  const Card = adminHomeCards.find((c) => c.id === "onboarding")!.Component;

  it("N/12 완료와 앞선 미완료 항목 3개 바로가기를 보인다", async () => {
    get.mockResolvedValue(sample);
    renderIn(<Card />);
    expect(await screen.findByText("도입 점검: 1/12 완료")).toBeInTheDocument();
    const links = screen.getAllByRole("link").map((a) => a.textContent);
    expect(links).toContain("조직도");
    expect(links).toContain("기초 데이터");
    expect(links).toContain("키 설정");
    expect(links).not.toContain("회사 정보"); // 완료된 항목은 나열하지 않는다
    expect(links).not.toContain("알림 설정"); // 해당 없음도 나열하지 않는다
  });

  it("모두 완료·해당 없음이면 한 줄로 접힌다", async () => {
    get.mockResolvedValue({ items: [item({ id: "a", title: "A", state: "done" }), item({ id: "b", title: "B", state: "not_applicable" })], summary: { total: 12, done: 1, notApplicable: 1 } });
    renderIn(<Card />);
    expect(await screen.findByText(/도입 점검을 모두 마쳤습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/완료$/)).toBeNull();
  });

  it("권한이 onboarding:read 이고 점검표 모듈 순서가 맨 위다", () => {
    const c = adminHomeCards.find((x) => x.id === "onboarding")!;
    expect(c.permission).toBe("onboarding:read");
    expect(Math.min(...adminHomeCards.map((x) => x.order))).toBe(c.order);
  });
});

describe("백업 상태 화면", () => {
  const status = {
    available: true,
    last: {
      backup: { id: 3, kind: "backup", startedAt: "2026-11-12T00:00:00Z", finishedAt: "2026-11-12T00:01:00Z", result: "success", setName: "pl-20261112T000000Z", dbKind: "sqlite", bytes: 2048, chainSeq: 77 },
    },
    lastFailed: { backup: { id: 5, kind: "backup", startedAt: "2026-11-13T00:00:00Z", finishedAt: "2026-11-13T00:01:00Z", result: "failed", setName: "", dbKind: "sqlite", bytes: 0, chainSeq: 0, errorCode: "disk_full" } },
    recent: [
      { id: 5, kind: "backup", startedAt: "2026-11-13T00:00:00Z", finishedAt: "2026-11-13T00:01:00Z", result: "failed", setName: "", dbKind: "sqlite", bytes: 0, chainSeq: 0, errorCode: "disk_full" },
      { id: 3, kind: "backup", startedAt: "2026-11-12T00:00:00Z", finishedAt: "2026-11-12T00:01:00Z", result: "success", setName: "pl-20261112T000000Z", dbKind: "sqlite", bytes: 2048, chainSeq: 77 },
    ],
  };

  it("마지막 결과·실패 경고·최근 기록을 보이고 실행 단추는 없다", async () => {
    get.mockResolvedValue(status);
    renderIn(<BackupStatusPage />);
    expect(await screen.findByRole("heading", { name: "백업 상태" })).toBeInTheDocument();
    const last = screen.getByLabelText("마지막 백업");
    expect(within(last).getByText("성공")).toBeInTheDocument();
    expect(within(last).getByText(/사유 disk_full/)).toBeInTheDocument();
    expect(screen.getByText(/복구 리허설 기록이 없습니다/)).toBeInTheDocument();
    expect(screen.getByRole("table")).toHaveTextContent("pl-20261112T000000Z");
    expect(screen.queryByRole("button")).toBeNull(); // 읽기 전용(D16)
    expect(screen.getByText(/이 화면에서는 실행할 수 없습니다/)).toBeInTheDocument();
  });

  it("기록이 없으면 안내하고, 표를 읽을 수 없으면 빈 상태 문구를 보인다", async () => {
    get.mockResolvedValue({ available: true, last: {}, lastFailed: {}, recent: [] });
    const { unmount } = renderIn(<BackupStatusPage />);
    expect(await screen.findByText(/성공한 백업 기록이 없습니다/)).toBeInTheDocument();
    expect(screen.getByText("아직 기록이 없습니다.")).toBeInTheDocument();
    unmount();
    get.mockResolvedValue({ available: false, last: {}, lastFailed: {}, recent: [] });
    renderIn(<BackupStatusPage />);
    expect(await screen.findByText("백업 기록을 읽을 수 없습니다.")).toBeInTheDocument();
  });
});
