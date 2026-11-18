import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { SettingsPage } from "./SettingsPage";

vi.mock("../api/client", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

beforeEach(() => {
  vi.mocked(api.get).mockReset();
  vi.mocked(api.put).mockReset();
});

describe("관리자 설정 화면", () => {
  it("이미지 전송은 꺼진 상태로 보이고 켜면 PUT 으로 저장한다", async () => {
    vi.mocked(api.get).mockImplementation(async (url: string) => {
      if (url === "/api/llm/policies") return { policies: [] };
      if (url === "/api/llm/quotas") return { quotas: [] };
      return { enabled: false };
    });
    vi.mocked(api.put).mockResolvedValue({ enabled: true });
    render(<SettingsPage />);
    const box = await screen.findByRole("checkbox", { name: /이미지 증빙의 AI 전송 허용/ });
    expect(box).not.toBeChecked();
    fireEvent.click(box);
    await waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/admin/settings/llm-image", { enabled: true }));
    await waitFor(() => expect(box).toBeChecked());
  });
});
