// 구매요청 상세의 발주 안내: 발주 번호·발주일, 발주서 링크, 입고 권한이 있을 때만 "입고 화면으로" 링크.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { PermissionGate } from "../auth/PermissionGate";
import { Notice } from "../layout/ui";
import { useLoad } from "./useLoad";

interface POInfo {
  poNo: string;
  poDate: string;
}

/** 발주일은 "2026. 11. 4." 모양이다. */
function poDay(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString("ko-KR");
}

export function OrderNotice({ prId, purchaseOrderId }: { prId: number; purchaseOrderId: number }) {
  // 발주 번호·발주일은 발주서 조회(구매 조회 권한)에서 얻는다. 못 읽어도 안내와 발주서 링크는 보인다.
  const po = useLoad(async () => {
    try {
      return await api.get<POInfo>(`/api/purchase-requests/${prId}/po`);
    } catch {
      return null;
    }
  }, [prId]);
  const day = po.data?.poDate ? poDay(po.data.poDate) : "";
  const detail = [po.data?.poNo ? `발주 ${po.data.poNo}` : "", day ? `발주일 ${day}` : ""].filter(Boolean);
  return (
    <Notice tone="ok">
      발주가 생성되었습니다. 입고 화면에서 입고를 처리할 수 있습니다.
      {" "}
      ({detail.map((t) => `${t} · `).join("")}
      <PermissionGate permission="inventory:manage">
        <Link to={`/receipts?po=${purchaseOrderId}`}>입고 화면으로</Link>
        {" · "}
      </PermissionGate>
      <Link to={`/purchase-requests/${prId}/po`}>발주서</Link>)
    </Notice>
  );
}
