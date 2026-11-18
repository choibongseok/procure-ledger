import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { AuthProvider } from "../auth/AuthProvider";
import { AuditLogPage } from "./AuditLogPage";
import { actorText, channelText, diffItemText, targetIdText } from "./auditLabels";

vi.mock("../api/client", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn(), put: vi.fn() },
  setCsrfToken: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}));

describe("감사 로그 값 표시", () => {
  it("결재 단계의 역할·직책 코드는 한국어로 보인다", () => {
    expect(diffItemText({ key: "step", new: "3/finance" })).toBe("결재 단계: 3단계 재무");
    expect(diffItemText({ key: "step", new: "2/dept_head" })).toBe("결재 단계: 2단계 부서장");
    expect(diffItemText({ key: "step", new: "1/team_lead" })).toBe("결재 단계: 1단계 팀장");
  });

  it("규정 검색 기록의 키에 라벨이 붙고 대상 related 가 한국어로 보인다", () => {
    expect(targetIdText("policy", "related")).toBe("작성 화면 관련 조항");
    expect(targetIdText("policy", "search")).toBe("질의 검색");
    expect(targetIdText("policy", "0")).toBe("대상 없음");
    // unix 분 숫자는 날짜·시각으로 보인다(숫자 그대로 나오지 않는다).
    const when = diffItemText({ key: "window_start", new: "29400000" });
    expect(when).toMatch(/^집계 시작: 20\d\d\. /);
    expect(when).not.toContain("29400000");
    // ISO 원문(위임 시작·종료, 토큰 만료 시각)도 날짜·시각으로 보인다.
    for (const key of ["expires_at", "start_at", "end_at"]) {
      const t = diffItemText({ key, new: "2026-11-10T00:00:00Z" });
      expect(t).not.toMatch(/\d{4}-\d{2}-\d{2}T/);
      expect(t).toMatch(/20\d\d\. /);
    }
    expect(diffItemText({ key: "amount", new: "10000000" })).toBe("금액: 10,000,000원");
    expect(diffItemText({ key: "items", new: "볼트 M8" })).toBe("품목: 볼트 M8");
    expect(diffItemText({ key: "count", new: "5" })).toBe("결과 건수: 5");
    expect(diffItemText({ key: "query", new: "결재선" })).toBe("질의어: 결재선");
  });

  it("문서 지문과 16진 해시는 값 대신 '기록됨'만 보인다", () => {
    expect(diffItemText({ key: "sha256", new: "0ca0588a1b2c3d4e" })).toBe("문서 지문: 기록됨");
    expect(diffItemText({ key: "something", new: "0ca0588a1b2c3d4e5f60…" })).toBe("항목: 기록됨");
  });

  it("B13 키: 토큰은 이름·앞자리, 소요 시간은 ms", () => {
    expect(diffItemText({ key: "token_id", new: "4" }, { tokenLabel: "내 토큰 (plp_ab12…)" })).toBe("개인 토큰: 내 토큰 (plp_ab12…)");
    expect(diffItemText({ key: "token_id", new: "99" })).toBe("개인 토큰: 삭제되었거나 알 수 없는 토큰");
    expect(diffItemText({ key: "duration_ms", new: "12" })).toBe("소요 시간(ms): 12ms");
  });

  it("사용자 칸은 이름과 역할, 이름을 모르면 번호, 번호 0 은 시스템", () => {
    expect(actorText({ actorId: 8, actorName: "구매직원", actorRole: "staff" })).toBe("구매직원 (직원)");
    expect(actorText({ actorId: 8, actorRole: "staff" })).toBe("사용자 #8 (직원)");
    expect(actorText({ actorId: 0, actorRole: "admin" })).toBe("시스템");
  });

  it("화면 표에 코드·해시·번호만 있는 사용자 칸이 나오지 않는다", async () => {
    const rec = { at: "2026-11-10T01:02:03Z", channel: "web", result: "success", detail: "", prevHash: "a", hash: "b" };
    vi.mocked(api.get).mockResolvedValue({
      records: [
        { ...rec, seq: 2, actorId: 8, actorName: "구매직원", actorRole: "staff", action: "approval.approve", targetType: "approval", targetId: "7",
          diff: [{ key: "step", new: "3/finance" }] },
        { ...rec, seq: 1, actorId: 8, actorName: "구매직원", actorRole: "staff", action: "policy.search", targetType: "policy", targetId: "related",
          diff: [{ key: "amount", new: "10000000" }, { key: "items", new: "볼트 M8" }, { key: "count", new: "5" }] },
        { ...rec, seq: 3, actorId: 8, actorName: "구매직원", actorRole: "staff", action: "document.verify", targetType: "document", targetId: "1",
          diff: [{ key: "sha256", new: "0ca0588a1b2c3d4e" }] },
      ],
      nextBeforeSeq: 0,
    });
    const { container } = render(
      <AuthProvider initialUser={{ id: 3, name: "감사", role: "auditor", departmentId: 1 }}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findAllByText("구매직원 (직원)");
    const text = container.querySelector("tbody")!.textContent ?? "";
    for (const leak of ["team_lead", "dept_head", "finance", "related", "0ca0588a", "#8"]) expect(text, leak).not.toContain(leak);
    expect(text).toContain("3단계 재무");
    expect(text).toContain("작성 화면 관련 조항");
    expect(text).toContain("결과 건수: 5");
    expect(text).toContain("문서 지문: 기록됨");
  });
});

describe("감사 로그 금액·같은 값·비고 표시", () => {
  it("금액 키는 천 단위 구분과 원을 붙인다", () => {
    expect(diffItemText({ key: "total_amount", new: "10000000" })).toBe("총액: 10,000,000원");
    expect(diffItemText({ key: "total_amount", old: "999", new: "1234567" })).toBe("총액: 999원 → 1,234,567원");
    expect(diffItemText({ key: "amount", new: "0" })).toBe("금액: 0원");
  });

  it("이전·이후가 같으면 화살표 없이 값 하나만 보인다", () => {
    expect(diffItemText({ key: "round", old: "1", new: "1" })).toBe("상신 차수: 1");
    expect(diffItemText({ key: "round", old: "1", new: "2" })).toBe("상신 차수: 1 → 2");
  });

  it("동작 이름과 같은 비고는 되풀이하지 않는다", async () => {
    const rec = { at: "2026-11-10T01:02:03Z", channel: "web", result: "success", prevHash: "a", hash: "b", actorId: 8, actorName: "재무 담당", actorRole: "finance" };
    vi.mocked(api.get).mockResolvedValue({
      records: [{ ...rec, seq: 1, action: "vendor.create", targetType: "vendor", targetId: "1", detail: "거래처 등록", diff: [{ key: "name", new: "한빛상사" }] }],
      nextBeforeSeq: 0,
    });
    const { container } = render(
      <AuthProvider initialUser={{ id: 3, name: "감사", role: "auditor", departmentId: 1 }}>
        <AuditLogPage />
      </AuthProvider>,
    );
    await screen.findByText("재무 담당 (재무)");
    const cells = container.querySelectorAll("tbody tr td");
    expect(cells[7].textContent).toBe("이름: 한빛상사");
  });
});

describe("시스템 행위자 표시(AC-69c)", () => {
  it("행위자가 시스템이면 경로 칸도 시스템으로 보이고 사람의 웹 기록은 웹으로 보인다", () => {
    expect(channelText({ actorId: 0, actorRole: "system", channel: "web" })).toBe("시스템");
    expect(actorText({ actorId: 0, actorRole: "system" })).toBe("시스템");
    expect(channelText({ actorId: 7, actorRole: "admin", channel: "web" })).toBe("웹");
    expect(channelText({ actorId: 0, actorRole: "", channel: "llmgw" })).toBe("AI 호출");
  });
});
