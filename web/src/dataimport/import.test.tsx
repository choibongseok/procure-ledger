import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { ROLE_PERMISSIONS, type Role } from "../auth/permissions";
import { visibleMenu } from "../layout/Layout";
import { importApi, type Batch, type Preview, type TargetStatus } from "./api";
import { ImportHistoryPage } from "./ImportHistoryPage";
import { ImportHubPage } from "./ImportHubPage";
import { ImportTargetPage } from "./ImportTargetPage";
import { dataImportModule } from "./routes";

const user = (role: Role): CurrentUser => ({ id: 1, name: "사용자", role, departmentId: 1 });
const counts = { total: 3, add: 2, same: 1, change: 0, error: 0, warn: 1 };

function target(over: Partial<TargetStatus> = {}): TargetStatus {
  return { target: "warehouse", name: "창고", needsBaseDate: false, keyRule: "창고는 코드가 같으면 같은 창고로 봅니다.", allowed: true, ready: true, lastAdded: 0, lastChanged: 0,
    columns: [{ label: "코드", required: true, help: "영문·숫자 2~20자", example: "WH-02" }], ...over };
}

function preview(over: Partial<Preview> = {}): Preview {
  return { batchNo: "IMP-2026-0001", token: "tok", target: "warehouse", counts, applicable: true, keyRule: "창고는 코드가 같으면 같은 창고로 봅니다.", summary: ["창고 3곳"], rows: [
    { rowNo: 2, key: "WH-01", verdict: "add", warn: "위치가 비어 있습니다." }, { rowNo: 3, key: "WH-02", verdict: "add" }, { rowNo: 4, key: "WH-03", verdict: "same" }], rowsTruncated: false, ...over };
}

function mountAt(path: string, role: Role = "finance") {
  return render(
    <AuthProvider initialUser={user(role)}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/admin/import" element={<ImportHubPage />} />
          <Route path="/admin/import/history" element={<ImportHistoryPage />} />
          <Route path="/admin/import/:target" element={<ImportTargetPage />} />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  );
}

const xlsx = () => new File([new Uint8Array([80, 75, 3, 4])], "창고.xlsx");

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("기초 데이터 가져오기 허브", () => {
  it("대상 카드를 순서대로 보이고, 권한이 없거나 연결되지 않은 대상은 사유와 함께 비활성이다", async () => {
    vi.spyOn(importApi, "targets").mockResolvedValue([
      target({ target: "vendor", name: "거래처", lastBatchNo: "IMP-2026-0003", lastAdded: 5, lastChanged: 1, lastAt: "2026-11-12T01:00:00Z" }),
      target({ target: "account", name: "계정과목" }),
      target({ target: "opening_stock", name: "기초 재고", allowed: false, reason: "이 대상을 가져올 권한이 없습니다." }),
      target({ target: "item", name: "품목", ready: false, reason: "이 대상의 반영 기능이 아직 연결되지 않았습니다." }),
    ]);
    mountAt("/admin/import");
    const heads = (await screen.findAllByRole("heading", { level: 3 })).map((h) => h.textContent);
    expect(heads).toEqual(["1. 계정과목", "2. 거래처", "3. 품목", "4. 기초 재고"]);
    expect(screen.getByText(/마지막 반영 IMP-2026-0003 · 추가 5건 · 변경 1건/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "계정과목 가져오기" })).toHaveAttribute("href", "/admin/import/account");
    expect(screen.getByRole("button", { name: "기초 재고 가져오기" })).toBeDisabled();
    expect(screen.getByText("이 대상을 가져올 권한이 없습니다.")).toBeInTheDocument();
    expect(screen.getByText("이 대상의 반영 기능이 아직 연결되지 않았습니다.")).toBeInTheDocument();
    expect(screen.queryByText(/ledger|account_no|import:/)).toBeNull();
  });
});

describe("대상별 가져오기 단계 화면", () => {
  it("파일을 올려 미리보기를 보고 확인한 뒤 반영한다(반영 때 같은 파일과 토큰을 보낸다)", async () => {
    vi.spyOn(importApi, "targets").mockResolvedValue([target()]);
    const pv = vi.spyOn(importApi, "preview").mockResolvedValue(preview());
    const ap = vi.spyOn(importApi, "apply").mockResolvedValue({ no: "IMP-2026-0001", target: "warehouse", targetName: "창고", status: "applied", counts, createdBy: "사용자", createdAt: "2026-11-12T01:00:00Z", journalNos: ["JV-2026-0009"] } satisfies Batch);
    vi.spyOn(importApi, "cancel").mockResolvedValue(undefined);
    mountAt("/admin/import/warehouse");
    expect(await screen.findByRole("heading", { name: "창고 가져오기" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "미리보기" })).toBeDisabled();
    expect(screen.getByRole("list", { name: "진행 단계" })).toHaveTextContent("1. 양식 내려받기");
    const f = xlsx();
    fireEvent.change(screen.getByLabelText("가져올 파일"), { target: { files: [f] } });
    expect(screen.getByTestId("import-file-name")).toHaveTextContent("선택한 파일: 창고.xlsx");
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByRole("heading", { name: "3. 미리보기" })).toBeInTheDocument();
    expect(pv).toHaveBeenCalledWith("warehouse", f, "");
    expect(screen.getByLabelText("건수 요약")).toHaveTextContent("추가 2건");
    expect(screen.getByLabelText("건수 요약")).toHaveTextContent("경고 1건");
    expect(screen.getAllByText("창고는 코드가 같으면 같은 창고로 봅니다.").length).toBe(2); // 양식 단계 안내 + 미리보기 위 중복 판정 기준 문장
    expect(screen.getByText("창고 3곳")).toBeInTheDocument();
    expect(screen.getByText("경고: 위치가 비어 있습니다.")).toBeInTheDocument();
    expect(screen.queryByText("add")).toBeNull(); // 영문 코드 노출 없음
    fireEvent.click(screen.getByRole("button", { name: "반영하기" }));
    expect(await screen.findByText(/2건을 반영합니다\. 되돌릴 수 없습니다\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "반영" }));
    await waitFor(() => expect(ap).toHaveBeenCalledWith("warehouse", "IMP-2026-0001", "tok", f));
    expect(await screen.findByText(/IMP-2026-0001 을\(를\) 반영했습니다/)).toBeInTheDocument();
    expect(screen.getByText("JV-2026-0009")).toBeInTheDocument();
  });

  it("오류가 있으면 반영할 수 없고 오류 파일을 받는다", async () => {
    vi.spyOn(importApi, "targets").mockResolvedValue([target()]);
    vi.spyOn(importApi, "preview").mockResolvedValue(
      preview({ token: "", applicable: false, counts: { ...counts, error: 1, add: 1 }, message: "오류 행 1건이 있어 반영할 수 없습니다. 오류 파일을 내려받아 고친 뒤 다시 올리세요.", rows: [{ rowNo: 3, key: "", verdict: "error", reason: "코드: 영문·숫자·하이픈 2~20자여야 합니다." }] }),
    );
    const ef = vi.spyOn(importApi, "errorFile").mockResolvedValue("오류.xlsx");
    mountAt("/admin/import/warehouse");
    await screen.findByRole("heading", { name: "창고 가져오기" });
    fireEvent.change(screen.getByLabelText("가져올 파일"), { target: { files: [xlsx()] } });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText(/오류 행 1건이 있어 반영할 수 없습니다/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "반영하기" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "오류 파일 내려받기" }));
    await waitFor(() => expect(ef).toHaveBeenCalledWith("warehouse", "IMP-2026-0001"));
    expect(await screen.findByText(/오류\.xlsx 을\(를\) 내려받았습니다/)).toBeInTheDocument();
  });

  it("기초 재고 같은 대상은 기준일을 입력해야 하고 서버 오류는 업무 문구로 보인다", async () => {
    vi.spyOn(importApi, "targets").mockResolvedValue([target({ target: "opening_stock", name: "기초 재고", needsBaseDate: true })]);
    const { ApiError } = await import("../api/client");
    const pv = vi.spyOn(importApi, "preview").mockRejectedValue(new ApiError(400, "입력값이 올바르지 않습니다: 마감된 월입니다. 재무팀에 마감 해제를 요청하세요."));
    mountAt("/admin/import/opening_stock");
    await screen.findByRole("heading", { name: "기초 재고 가져오기" });
    fireEvent.change(screen.getByLabelText(/기준일/), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("가져올 파일"), { target: { files: [xlsx()] } });
    fireEvent.click(screen.getByRole("button", { name: "미리보기" }));
    expect(await screen.findByText("마감된 월입니다. 재무팀에 마감 해제를 요청하세요.")).toBeInTheDocument();
    expect(pv).toHaveBeenCalledWith("opening_stock", expect.any(File), "2026-11-01");
  });

  it("권한이 없는 대상은 사유만 보이고 입력이 없다", async () => {
    vi.spyOn(importApi, "targets").mockResolvedValue([target({ allowed: false, reason: "이 대상을 가져올 권한이 없습니다." })]);
    mountAt("/admin/import/warehouse", "admin");
    expect(await screen.findByText("이 대상을 가져올 권한이 없습니다.")).toBeInTheDocument();
    expect(screen.queryByLabelText("가져올 파일")).toBeNull();
  });
});

describe("가져오기 이력", () => {
  const batch = (over: Partial<Batch> = {}): Batch => ({ no: "IMP-2026-0002", target: "vendor", targetName: "거래처", status: "applied", counts, createdBy: "재무 담당", createdAt: "2026-11-12T01:00:00Z", appliedAt: "2026-11-12T01:05:00Z", ...over });

  it("묶음 표와 상세를 보이고 조건을 바꾸면 다시 조회한다", async () => {
    const h = vi.spyOn(importApi, "history").mockResolvedValue([batch()]);
    vi.spyOn(importApi, "batch").mockResolvedValue({ ...batch(), summary: ["거래처 5곳"], rows: [{ rowNo: 2, key: "123-**-***89", verdict: "change", changes: [{ column: "계좌번호", old: "****6789", new: "****1111" }] }], truncated: false });
    mountAt("/admin/import/history", "auditor");
    expect(await screen.findByText("IMP-2026-0002")).toBeInTheDocument();
    expect(screen.getAllByText("반영됨").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "상세" }));
    expect(await screen.findByText("계좌번호: ****6789 → ****1111")).toBeInTheDocument();
    expect(screen.getByText("거래처 5곳")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("상태"), { target: { value: "all" } });
    await waitFor(() => expect(h).toHaveBeenLastCalledWith("all", ""));
  });

  it("묶음이 없으면 빈 상태 문구를 보인다", async () => {
    vi.spyOn(importApi, "history").mockResolvedValue([]);
    mountAt("/admin/import/history", "finance");
    expect(await screen.findByText("조건에 맞는 가져오기 묶음이 없습니다.")).toBeInTheDocument();
  });
});

describe("메뉴 노출", () => {
  const labels = (role: Role) => visibleMenu(dataImportModule.menu, (p) => p === "authenticated" || p === "public" || (ROLE_PERMISSIONS[role] as readonly string[]).includes(p)).map((m) => m.label);
  it("마스터·금액성 권한이 있으면 가져오기 메뉴가, 조회 권한이 있으면 이력이 보이고 직원·결재자에게는 없다", () => {
    expect(labels("admin")).toEqual(["기초 데이터 가져오기", "가져오기 이력"]);
    expect(labels("finance")).toEqual(["기초 데이터 가져오기", "가져오기 이력"]);
    expect(labels("auditor")).toEqual(["가져오기 이력"]);
    expect(labels("staff")).toEqual([]);
    expect(labels("manager")).toEqual([]);
  });
});
