import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { AuthProvider, type CurrentUser } from "./AuthProvider";
import { PermissionGate, RequirePermission } from "./PermissionGate";
import { PERMISSIONS, ROLES, ROLE_PERMISSIONS, roleHas } from "./permissions";

const staff: CurrentUser = { id: 1, name: "김직원", role: "staff", departmentId: 1 };

describe("permissions", () => {
  it("모든 역할 표 항목이 알려진 권한이다", () => {
    for (const r of ROLES) for (const p of ROLE_PERMISSIONS[r]) expect(PERMISSIONS).toContain(p);
  });
  it("알 수 없는 역할은 거부한다(fail-closed)", () => {
    expect(roleHas("ghost", "purchase:read")).toBe(false);
    expect(roleHas(undefined, "authenticated")).toBe(false);
  });
  it("staff 는 결재 불가, auditor 는 감사 로그 조회 가능", () => {
    expect(roleHas("staff", "approval:decide")).toBe(false);
    expect(roleHas("auditor", "audit:read")).toBe(true);
    expect(roleHas("auditor", "purchase:create")).toBe(false);
  });
});

describe("서버가 준 실효 권한(B33)", () => {
  it("권한 목록이 있으면 역할 표 대신 그 목록으로만 판정한다(사용자 정의 역할)", () => {
    render(
      <AuthProvider initialUser={staff} initialPermissions={["audit:read"]}>
        <PermissionGate permission="audit:read" fallback={<span>감사 숨김</span>}>
          <button>감사 로그</button>
        </PermissionGate>
        <PermissionGate permission="purchase:create" fallback={<span>작성 숨김</span>}>
          <button>작성</button>
        </PermissionGate>
      </AuthProvider>,
    );
    expect(screen.getByText("감사 로그")).toBeInTheDocument(); // staff 역할 표에는 없는 권한
    expect(screen.getByText("작성 숨김")).toBeInTheDocument(); // staff 역할 표에는 있지만 서버 목록에 없음
  });
  it("빈 권한 목록이면 로그인 경로 외에는 모두 숨긴다", () => {
    render(
      <AuthProvider initialUser={staff} initialPermissions={[]}>
        <PermissionGate permission="purchase:read" fallback={<span>숨김</span>}>
          <button>조회</button>
        </PermissionGate>
        <PermissionGate permission="authenticated">
          <button>내 정보</button>
        </PermissionGate>
      </AuthProvider>,
    );
    expect(screen.getByText("숨김")).toBeInTheDocument();
    expect(screen.getByText("내 정보")).toBeInTheDocument();
  });
});

describe("PermissionGate", () => {
  it("권한이 있을 때만 자식을 그린다", () => {
    render(
      <AuthProvider initialUser={staff}>
        <PermissionGate permission="approval:decide" fallback={<span>숨김</span>}>
          <button>승인</button>
        </PermissionGate>
        <PermissionGate permission="purchase:create">
          <button>작성</button>
        </PermissionGate>
      </AuthProvider>,
    );
    expect(screen.queryByText("승인")).toBeNull();
    expect(screen.getByText("숨김")).toBeInTheDocument();
    expect(screen.getByText("작성")).toBeInTheDocument();
  });

  it("RequirePermission 은 권한 없음 안내를 보여 준다", () => {
    render(
      <MemoryRouter>
        <AuthProvider initialUser={staff}>
          <RequirePermission permission="audit:read">
            <p>비밀</p>
          </RequirePermission>
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.queryByText("비밀")).toBeNull();
    expect(screen.getByRole("alert")).toHaveTextContent("권한이 없습니다");
  });
});
