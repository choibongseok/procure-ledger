import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { EntitySwitcher, GroupModeBanner, entityKey } from "./EntitySwitcher";

const switchEntity = vi.fn();
let auth: Record<string, unknown> = {};
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const two = [
  { id: 1, code: "MAIN", name: "에이엑스 주식회사" },
  { id: 2, code: "ABC", name: "계열사" },
];

beforeEach(() => {
  switchEntity.mockReset();
  switchEntity.mockResolvedValue(undefined);
  auth = { entities: two, currentEntity: { all: false, id: 1, code: "MAIN", name: "에이엑스 주식회사" }, groupScope: false, switchEntity };
});

describe("법인 선택기", () => {
  it("법인이 하나뿐이거나 법인 정보가 없으면 아무것도 그리지 않는다(화면이 이전과 같다)", () => {
    auth = { entities: [two[0]], currentEntity: { all: false, code: "MAIN" }, groupScope: false, switchEntity };
    const { container, rerender } = render(<EntitySwitcher />);
    expect(container).toBeEmptyDOMElement();
    auth = {};
    rerender(<EntitySwitcher />);
    expect(container).toBeEmptyDOMElement();
  });

  it("법인이 둘 이상이면 현재 법인을 보이고, 고르면 법인 코드로 바꾼다(전체 법인은 그룹 범위가 있을 때만)", async () => {
    const { rerender } = render(<EntitySwitcher />);
    const sel = screen.getByLabelText("현재 법인") as HTMLSelectElement;
    expect(sel.value).toBe("MAIN");
    expect(screen.queryByRole("option", { name: "전체 법인" })).toBeNull();
    fireEvent.change(sel, { target: { value: "ABC" } });
    await waitFor(() => expect(switchEntity).toHaveBeenCalledWith("ABC"));
    auth = { ...auth, groupScope: true, currentEntity: { all: true } };
    rerender(<EntitySwitcher />);
    expect((screen.getByLabelText("현재 법인") as HTMLSelectElement).value).toBe("*");
    expect(screen.getByRole("option", { name: "전체 법인" })).toBeInTheDocument();
  });

  it("바꾸기에 실패하면 사유를 한국어로 알린다", async () => {
    switchEntity.mockRejectedValue(new ApiError(403, "권한이 없습니다"));
    render(<EntitySwitcher />);
    fireEvent.change(screen.getByLabelText("현재 법인"), { target: { value: "ABC" } });
    expect(await screen.findByRole("alert")).toHaveTextContent("권한이 없습니다");
  });

  it("전체 법인 모드에서만 읽기 전용 배너가 나온다", () => {
    const { container, rerender } = render(<GroupModeBanner />);
    expect(container).toBeEmptyDOMElement();
    auth = { ...auth, currentEntity: { all: true } };
    rerender(<GroupModeBanner />);
    expect(screen.getByRole("status")).toHaveTextContent("전체 법인 조회 중(읽기 전용)");
    expect(entityKey({ all: true })).toBe("*");
    expect(entityKey({ all: false, code: "ABC" })).toBe("ABC");
  });
});
