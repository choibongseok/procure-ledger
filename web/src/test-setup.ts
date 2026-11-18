import "@testing-library/jest-dom/vitest";
import { configure } from "@testing-library/react";
import { createElement, type ComponentProps } from "react";
import { vi } from "vitest";

// findBy·waitFor 의 기본 대기 1초는 기계가 바쁠 때(검증 겹침) 부족해 간헐 실패를 만든다. 성공하는 시험은 느려지지 않는다.
configure({ asyncUtilTimeout: 8000 });

// 운영 AuthProvider 에는 역할 표로 권한을 채우는 길이 없다. 시험이 initialUser 만 주면 그 기본 역할의 권한을 시험 도우미가 initialPermissions 로 채워 준다.
vi.mock("./auth/AuthProvider", async (orig) => {
  const mod = await orig<typeof import("./auth/AuthProvider")>();
  const { ROLE_PERMISSIONS } = await import("./auth/permissions");
  const TestAuthProvider = (props: ComponentProps<typeof mod.AuthProvider>) =>
    createElement(mod.AuthProvider, { ...props, initialPermissions: props.initialPermissions ?? (props.initialUser ? [...ROLE_PERMISSIONS[props.initialUser.role]] : undefined) });
  return { ...mod, AuthProvider: TestAuthProvider };
});
