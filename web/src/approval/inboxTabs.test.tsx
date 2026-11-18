import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { AuthProvider, type CurrentUser } from "../auth/AuthProvider";
import { businessMessage, errorText } from "../purchase/useLoad";
import { ApprovalInbox } from "./ApprovalInbox";

vi.mock("../api/client", async (orig) => {
  const mod = await orig<typeof import("../api/client")>();
  return { ...mod, setUnauthorizedHandler: vi.fn(), setCsrfToken: vi.fn(), api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
const get = (api as unknown as { get: ReturnType<typeof vi.fn> }).get;

const manager: CurrentUser = { id: 2, name: "김팀장", role: "manager", departmentId: 1 };
const pr = (id: number, status: string) => ({
  id, no: `PR-00000${id}`, title: `건${id}`, status, requesterName: "김직원", requesterUnitPath: "구매팀", totalAmount: 1000, steps: [],
});

beforeEach(() => get.mockReset());

describe("결재함 탭", () => {
  it("대기·진행·완료 탭이 각자 목록을 불러오고 빈 상태 문구가 다르다", async () => {
    get.mockImplementation(async (path: string = "") => {
      if (path === "/api/approvals/pending") return [pr(1, "submitted")];
      if (path === "/api/approvals/inbox?tab=progress") return [pr(2, "in_review")];
      if (path === "/api/approvals/inbox?tab=done") return [];
      if (path.startsWith("/api/approvals/delegations")) return path.endsWith("candidates") ? [] : { given: [], received: [] };
      return [];
    });
    render(
      <MemoryRouter>
        <AuthProvider initialUser={manager}>
          <ApprovalInbox />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText("건1")).toBeTruthy();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["대기", "진행", "완료"]);
    fireEvent.click(screen.getByRole("tab", { name: "진행" }));
    expect(await screen.findByText("건2")).toBeTruthy();
    expect(screen.getByRole("tab", { name: "진행" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(screen.getByRole("tab", { name: "완료" }));
    expect(await screen.findByText("완료된 결재가 없습니다.")).toBeTruthy();
  });
});

describe("사용자 오류 문구", () => {
  it("서버식 머리말을 떼고 다음 할 일을 붙인다", () => {
    expect(businessMessage("권한이 없습니다: 같은 구매요청의 다른 결재 단계를 이미 결재하셨습니다. 한 사람이 두 단계를 결재할 수 없습니다")).toBe(
      "같은 구매요청의 다른 결재 단계를 이미 결재하셨습니다. 한 사람이 두 단계를 결재할 수 없으니 다음 결재자의 처리를 기다리세요.",
    );
    expect(businessMessage("현재 상태에서는 할 수 없는 동작입니다: 결재할 수 있는 결재자가 있어 회수할 수 없습니다")).toMatch(/^회수하지 못했습니다\..*새로 고쳐/);
    expect(businessMessage("입력값이 올바르지 않습니다: 반려 사유를 입력해야 합니다")).toBe("반려 사유를 입력해야 합니다. 사유를 적은 뒤 다시 반려하세요.");
    expect(errorText(new ApiError(403, "본인이 올린 요청은 결재할 수 없습니다"))).toMatch(/관리자에게 결재자 변경을 요청하세요\.$/);
    expect(errorText(new ApiError(400, "입력값이 올바르지 않습니다: 제목은 1자 이상 200자 이하여야 합니다"))).toBe("제목은 1자 이상 200자 이하여야 합니다");
  });
});
