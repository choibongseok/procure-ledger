// 재고 화면 위 탭(B24): 재고·출고·창고 이동·실사·수불부. 권한 있는 탭만 그린다(새 메뉴를 만들지 않는다).
import { NavLink } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";

const TABS: { path: string; label: string; permission: "inventory:read" | "inventory:manage"; end?: boolean }[] = [
  { path: "/inventory", label: "재고", permission: "inventory:read", end: true },
  { path: "/inventory/issues", label: "출고", permission: "inventory:manage" },
  { path: "/inventory/transfers", label: "창고 이동", permission: "inventory:manage" },
  { path: "/inventory/counts", label: "실사", permission: "inventory:manage" },
  { path: "/inventory/ledger", label: "수불부", permission: "inventory:read" },
  { path: "/inventory/close", label: "월 마감", permission: "inventory:read" },
  { path: "/inventory/month-stock", label: "월별 재고 현황", permission: "inventory:read" },
];

export function InventoryTabs() {
  const { can } = useAuth();
  return (
    <nav className="form-row" aria-label="재고 탭">
      {TABS.filter((t) => can(t.permission)).map((t) => (
        <NavLink key={t.path} to={t.path} end={t.end} className={({ isActive }) => `btn ${isActive ? "" : "btn-secondary"}`}>
          {t.label}
        </NavLink>
      ))}
    </nav>
  );
}
