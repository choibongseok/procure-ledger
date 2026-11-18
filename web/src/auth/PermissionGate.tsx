// 권한 게이트 컴포넌트. 화면 노출 제어일 뿐, 강제는 서버가 한다.
import type { ReactNode } from "react";
import { Navigate } from "react-router-dom";
import { Notice } from "../layout/ui";
import { useAuth } from "./AuthProvider";
import type { PermissionOrOpen } from "./permissions";

/** 권한이 있을 때만 자식을 그린다. 없으면 fallback(기본: 아무것도 안 그림). */
export function PermissionGate({
  permission,
  fallback = null,
  children,
}: {
  permission: PermissionOrOpen;
  fallback?: ReactNode;
  children: ReactNode;
}) {
  const { can } = useAuth();
  return <>{can(permission) ? children : fallback}</>;
}

/** 여러 권한 중 하나라도 있으면 통과하는 라우트 가드. */
export function RequireAnyPermission({ permissions, children }: { permissions: PermissionOrOpen[]; children: ReactNode }) {
  const { user, loading, can } = useAuth();
  if (loading) return <p className="hint">불러오는 중…</p>;
  if (!user) return <Navigate to="/login" replace />;
  if (!permissions.some((p) => can(p))) return <Notice tone="error">이 화면을 볼 권한이 없습니다.</Notice>;
  return <>{children}</>;
}

/** 라우트 가드. 미로그인은 로그인 화면으로, 권한 없음은 안내 문구를 보여 준다. */
export function RequirePermission({ permission, children }: { permission: PermissionOrOpen; children: ReactNode }) {
  const { user, loading, can } = useAuth();
  if (loading) return <p className="hint">불러오는 중…</p>;
  if (!user) return <Navigate to="/login" replace />;
  if (!can(permission)) return <Notice tone="error">이 화면을 볼 권한이 없습니다.</Notice>;
  return <>{children}</>;
}
