import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiDownload } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { ExportButtons, exportQuery } from "./ExportButtons";

vi.mock("../api/client", async (orig) => {
  const real = await orig<typeof import("../api/client")>();
  return {
    ...real,
    setUnauthorizedHandler: vi.fn(),
    setCsrfToken: vi.fn(),
    apiDownload: vi.fn(),
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  };
});

const download = apiDownload as unknown as ReturnType<typeof vi.fn>;

function renderButtons(role: "admin" | "staff" | "auditor" | "finance", props: Partial<React.ComponentProps<typeof ExportButtons>> = {}) {
  return render(
    <AuthProvider initialUser={{ id: 1, name: "사용자", role, departmentId: 1 }}>
      <ExportButtons path="/api/things/export" {...props} />
    </AuthProvider>,
  );
}

// mockReset() 이 돌려주는 모의 함수를 beforeEach 의 정리 함수로 오해해 부르지 않게 중괄호로 감싼다.
beforeEach(() => {
  download.mockReset();
});

describe("내보내기 단추", () => {
  it("쿼리는 형식과 현재 적용된 조건만 담고 빈 값은 뺀다(AC-710)", () => {
    expect(
      exportQuery("xlsx", { status: "in_review", q: "", unitId: undefined, includeLeft: true, asOf: "2026-11-12", off: false }, false),
    ).toBe("format=xlsx&status=in_review&includeLeft=1&asOf=2026-11-12");
    expect(exportQuery("csv", {}, true)).toBe("format=csv&raw=1");
  });

  it("엑셀·CSV 단추가 형식과 조건을 보내 내려받는다", async () => {
    download.mockResolvedValue("파일.xlsx");
    renderButtons("staff", { params: { status: "approved", q: "" } });
    fireEvent.click(screen.getByRole("button", { name: "엑셀(XLSX)" }));
    await waitFor(() => expect(download).toHaveBeenCalledWith("/api/things/export?format=xlsx&status=approved"));
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    await waitFor(() => expect(download).toHaveBeenLastCalledWith("/api/things/export?format=csv&status=approved"));
  });

  it("목록이 비었으면 단추가 꺼지고 안내가 보인다", () => {
    renderButtons("staff", { disabled: true });
    expect(screen.getByRole("button", { name: "엑셀(XLSX)" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "CSV" })).toBeDisabled();
    expect(screen.getByText("내보낼 자료가 없습니다.")).toBeInTheDocument();
  });

  it("서버 오류 문구(상한 초과 등)를 그대로 보인다", async () => {
    download.mockRejectedValue(
      new ApiError(400, "조건에 맞는 자료가 12,340건이라 한 번에 내보낼 수 없습니다(최대 10,000건). 기간이나 조건을 좁혀 다시 내보내세요."),
    );
    renderButtons("staff");
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    const al = await screen.findByRole("alert");
    expect(al).toHaveTextContent("12,340건이라 한 번에 내보낼 수 없습니다");
    await waitFor(() => expect(screen.getByRole("button", { name: "CSV" })).toBeEnabled());
  });

  it("원문 포함 체크는 민감 열이 있고 mask:bypass 가 있을 때만 보인다", () => {
    const { unmount } = renderButtons("admin", { sensitive: true });
    expect(screen.getByLabelText("원문 포함(민감 정보)")).toBeInTheDocument();
    unmount();
    const second = renderButtons("finance", { sensitive: true });
    expect(screen.getByLabelText("원문 포함(민감 정보)")).toBeInTheDocument();
    second.unmount();
    for (const role of ["staff", "auditor"] as const) {
      const r = renderButtons(role, { sensitive: true });
      expect(screen.queryByLabelText("원문 포함(민감 정보)")).toBeNull();
      r.unmount();
    }
    renderButtons("admin"); // 민감 열이 없는 데이터셋
    expect(screen.queryByLabelText("원문 포함(민감 정보)")).toBeNull();
  });

  it("원문 포함을 체크하면 확인 대화상자를 거쳐야 보내고 취소하면 보내지 않는다", async () => {
    download.mockResolvedValue("파일.csv");
    renderButtons("admin", { sensitive: true });
    fireEvent.click(screen.getByLabelText("원문 포함(민감 정보)"));
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    const dlg = await screen.findByRole("alertdialog", { name: "원문을 포함해 내보낼까요?" });
    expect(dlg).toHaveTextContent("가리지 않은 연락처가 들어갑니다");
    expect(download).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "취소" }));
    expect(download).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "CSV" }));
    fireEvent.click(await screen.findByRole("button", { name: "원문 포함해 내보내기" }));
    await waitFor(() => expect(download).toHaveBeenCalledWith("/api/things/export?format=csv&raw=1"));
  });

  it("원문 포함을 체크하지 않으면 확인 없이 바로 보낸다(raw 없음)", async () => {
    download.mockResolvedValue("x");
    renderButtons("admin", { sensitive: true });
    fireEvent.click(screen.getByRole("button", { name: "엑셀(XLSX)" }));
    await waitFor(() => expect(download).toHaveBeenCalledWith("/api/things/export?format=xlsx"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
