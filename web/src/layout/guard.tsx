// 라우트 element 를 권한 가드로 감싸는 헬퍼.
import type { ReactNode } from "react";
import { RequireAnyPermission, RequirePermission } from "../auth/PermissionGate";
import type { PermissionOrOpen } from "../auth/permissions";

export function guarded(permission: PermissionOrOpen, element: ReactNode): ReactNode {
  return <RequirePermission permission={permission}>{element}</RequirePermission>;
}

/** 여러 권한 중 하나라도 있으면 열리는 라우트(MenuItem.anyOf 와 짝). */
export function guardedAny(permissions: PermissionOrOpen[], element: ReactNode): ReactNode {
  return <RequireAnyPermission permissions={permissions}>{element}</RequireAnyPermission>;
}
