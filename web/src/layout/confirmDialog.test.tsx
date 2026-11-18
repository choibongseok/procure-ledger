import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "./ui";

function setup(busy: boolean, variant: "danger" | "primary" = "danger") {
  const onCancel = vi.fn();
  const r = render(
    <ConfirmDialog title="폐기" confirmLabel="폐기" onConfirm={vi.fn()} onCancel={onCancel} busy={busy} variant={variant}>
      설명
    </ConfirmDialog>,
  );
  return { onCancel, backdrop: r.container.querySelector(".dialog-backdrop") as HTMLElement };
}

describe("ConfirmDialog 취소 경로", () => {
  it("danger: Esc 는 취소, 바깥 클릭은 무시한다", () => {
    const { onCancel, backdrop } = setup(false);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    fireEvent.mouseDown(backdrop);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("primary: Esc 와 바깥 클릭이 onCancel 을 부른다", () => {
    const { onCancel, backdrop } = setup(false, "primary");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    fireEvent.mouseDown(backdrop);
    expect(onCancel).toHaveBeenCalledTimes(2);
  });

  it("busy=true 면 두 변형 모두 Esc 와 바깥 클릭이 onCancel 을 부르지 않는다", () => {
    for (const v of ["danger", "primary"] as const) {
      const { onCancel, backdrop } = setup(true, v);
      fireEvent.keyDown(document, { key: "Escape" });
      fireEvent.mouseDown(backdrop);
      expect(onCancel).not.toHaveBeenCalled();
    }
  });

  it("danger 는 alertdialog 이고 busy 중 aria-busy, primary 는 dialog 이고 aria-busy 없음", () => {
    const d = render(
      <ConfirmDialog title="a" confirmLabel="a" onConfirm={vi.fn()} onCancel={vi.fn()} busy>
        x
      </ConfirmDialog>,
    );
    expect(d.getByRole("alertdialog").getAttribute("aria-busy")).toBe("true");
    d.unmount();
    const p = render(
      <ConfirmDialog title="b" confirmLabel="b" variant="primary" onConfirm={vi.fn()} onCancel={vi.fn()} busy>
        x
      </ConfirmDialog>,
    );
    expect(p.getByRole("dialog").getAttribute("aria-busy")).toBeNull();
  });
});

describe("ConfirmDialog variant", () => {
  const confirmBtn = (variant?: "danger" | "primary") => {
    const r = render(
      <ConfirmDialog title="t" confirmLabel="확정" onConfirm={vi.fn()} onCancel={vi.fn()} variant={variant}>
        x
      </ConfirmDialog>,
    );
    return r.getByText("확정");
  };
  it("기본은 danger 클래스", () => {
    expect(confirmBtn().className).toBe("btn-danger");
  });
  it("primary 는 위험·보조 클래스 없는 기본 주 버튼", () => {
    const b = confirmBtn("primary");
    expect(b.classList.contains("btn-danger")).toBe(false);
    expect(b.classList.contains("btn-secondary")).toBe(false);
  });
});
