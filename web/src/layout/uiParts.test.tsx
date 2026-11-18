// 공용 부품 Pagination·LoadingRows·Stepper 시험.
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Breadcrumbs, EmptyState, FilterChips, LoadingRows, Pagination, SortHeader, Stepper, TableWrap, ToastProvider, useToast } from "./ui";

describe("Pagination", () => {
  it("한 쪽이면 그리지 않는다", () => {
    const { container } = render(<Pagination offset={0} limit={20} total={20} onChange={vi.fn()} />);
    expect(container.firstChild).toBeNull();
  });

  it("범위 문구와 이전·다음 이동", () => {
    const onChange = vi.fn();
    render(<Pagination offset={20} limit={20} total={45} onChange={onChange} />);
    expect(screen.getByRole("navigation", { name: "쪽 이동" })).toBeInTheDocument();
    expect(screen.getByText("21–40 / 전체 45건")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "이전" }));
    expect(onChange).toHaveBeenLastCalledWith(0);
    fireEvent.click(screen.getByRole("button", { name: "다음" }));
    expect(onChange).toHaveBeenLastCalledWith(40);
  });

  it("처음·끝에서 해당 단추가 꺼진다", () => {
    const { rerender } = render(<Pagination offset={0} limit={20} total={45} onChange={vi.fn()} />);
    expect(screen.getByRole("button", { name: "이전" })).toBeDisabled();
    rerender(<Pagination offset={40} limit={20} total={45} onChange={vi.fn()} />);
    expect(screen.getByRole("button", { name: "다음" })).toBeDisabled();
    expect(screen.getByText("41–45 / 전체 45건")).toBeInTheDocument();
  });
});

describe("LoadingRows·TableWrap busy", () => {
  it("열 수 x 행 수 칸을 그리고 표 상자에 aria-busy", () => {
    const { container } = render(
      <TableWrap busy>
        <table>
          <tbody>
            <LoadingRows cols={3} rows={2} />
          </tbody>
        </table>
      </TableWrap>,
    );
    expect(container.querySelectorAll("tr.loading-row")).toHaveLength(2);
    expect(container.querySelectorAll("tr.loading-row td")).toHaveLength(6);
    expect(container.querySelector(".table-wrap")?.getAttribute("aria-busy")).toBe("true");
  });

  it("busy 가 아니면 aria-busy 가 없다", () => {
    const { container } = render(<TableWrap>x</TableWrap>);
    expect(container.querySelector(".table-wrap")?.hasAttribute("aria-busy")).toBe(false);
  });
});

describe("Stepper", () => {
  it("지금 단계는 aria-current, 끝난 단계는 완료", () => {
    render(<Stepper steps={["가", "나", "다"]} current={2} />);
    expect(screen.getByText("1. 가 완료")).toBeInTheDocument();
    expect(screen.getByText("2. 나").closest("li")?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText("3. 다").closest("li")?.hasAttribute("aria-current")).toBe(false);
  });
});

describe("Breadcrumbs", () => {
  it("마지막은 링크 없이 aria-current, 구분자는 가린다", () => {
    render(
      <MemoryRouter>
        <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: "PR-1" }]} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "경로" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "구매요청" })).toHaveAttribute("href", "/purchase-requests");
    const cur = screen.getByText("PR-1");
    expect(cur).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("link", { name: "PR-1" })).toBeNull();
    expect(screen.getByText("›")).toHaveAttribute("aria-hidden", "true");
  });
});

describe("Toast", () => {
  afterEach(() => vi.useRealTimers());
  function Trigger() {
    const toast = useToast();
    return (
      <button type="button" onClick={() => toast("저장했습니다.")}>
        저장
      </button>
    );
  }
  it("role=status 로 알리고 4초 뒤 닫히며 × 로도 닫힌다", () => {
    vi.useFakeTimers();
    render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    expect(screen.getByRole("status")).toHaveTextContent("저장했습니다.");
    act(() => {
      vi.advanceTimersByTime(3900);
    });
    expect(screen.getByText("저장했습니다.")).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(screen.queryByText("저장했습니다.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    fireEvent.click(screen.getByRole("button", { name: "알림 닫기" }));
    expect(screen.queryByText("저장했습니다.")).toBeNull();
  });
  it("한 번에 하나만 보인다", () => {
    render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    fireEvent.click(screen.getByRole("button", { name: "저장" }));
    expect(screen.getAllByText("저장했습니다.")).toHaveLength(1);
  });
});

describe("EmptyState actions", () => {
  it("문구 아래에 버튼 줄을 그린다", () => {
    render(<EmptyState actions={<button type="button">새로 작성</button>}>없습니다.</EmptyState>);
    expect(screen.getByRole("button", { name: "새로 작성" })).toBeInTheDocument();
    const { container } = render(<EmptyState>없음</EmptyState>);
    expect(container.querySelector(".empty-actions")).toBeNull();
  });
});

describe("LoadingRows status", () => {
  it("화면에 안 보이는 불러오는 중 문구", () => {
    render(
      <table>
        <tbody>
          <LoadingRows cols={2} rows={1} />
        </tbody>
      </table>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("불러오는 중");
  });
});

describe("SortHeader", () => {
  it("aria-sort 와 정렬 요청", () => {
    const onSort = vi.fn();
    render(
      <table>
        <thead>
          <tr>
            <SortHeader label="금액" field="amount" sort="amount" order="desc" onSort={onSort} />
            <SortHeader label="작성일" field="created" sort="amount" order="desc" onSort={onSort} />
          </tr>
        </thead>
      </table>,
    );
    expect(screen.getByRole("columnheader", { name: /금액/ })).toHaveAttribute("aria-sort", "descending");
    expect(screen.getByRole("columnheader", { name: /작성일/ })).toHaveAttribute("aria-sort", "none");
    fireEvent.click(screen.getByRole("button", { name: /작성일/ }));
    expect(onSort).toHaveBeenCalledWith("created");
  });
  it("align=num 이면 머리글에 num 클래스", () => {
    render(
      <table>
        <thead>
          <tr>
            <SortHeader label="금액" field="amount" sort="" order="asc" onSort={vi.fn()} align="num" />
          </tr>
        </thead>
      </table>,
    );
    expect(screen.getByRole("columnheader", { name: /금액/ })).toHaveClass("num");
  });
});

describe("FilterChips", () => {
  it("칩이 없으면 그리지 않는다", () => {
    const { container } = render(<FilterChips chips={[]} onClearAll={vi.fn()} />);
    expect(container.firstChild).toBeNull();
  });
  it("조건 해제·조건 지우기", () => {
    const onClear = vi.fn();
    const onAll = vi.fn();
    render(<FilterChips chips={[{ label: "상태: 상신", onClear }]} onClearAll={onAll} />);
    fireEvent.click(screen.getByRole("button", { name: "조건 해제: 상태: 상신" }));
    expect(onClear).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "조건 지우기" }));
    expect(onAll).toHaveBeenCalled();
  });
});
