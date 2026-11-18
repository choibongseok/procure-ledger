// 구매요청 상세·결재 화면의 견적 카드(B25 ②): 선택한 거래처·사유·비교 화면 링크. 견적서가 없으면 한 줄만 보인다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge } from "../layout/ui";
import { won } from "./labels";
import type { QuoteCompare } from "./types";
import { useLoad } from "./useLoad";

/** editable 을 상세 응답에서 받으면 그 값만 쓴다(없으면 견적 비교 응답의 값). 서버는 둘을 같은 함수로 계산한다. */
export function QuoteCard({ prId, editable }: { prId: number; editable?: boolean }) {
  const cmp = useLoad(() => api.get<QuoteCompare>(`/api/purchase-requests/${prId}/quotes`), [prId]);
  if (!cmp.data || !Array.isArray(cmp.data.quotes)) return null; // 불러오지 못하면(권한 밖 등) 카드를 보이지 않는다
  const c = cmp.data;
  const sel = c.quotes.find((q) => q.selected);
  return (
    <aside className="card" aria-label="견적 비교">
      <h3>견적 비교</h3>
      {c.quotes.length === 0 ? (
        <p>첨부된 견적서가 없습니다.</p>
      ) : (
        <>
          <p>
            견적서 {c.quotes.length}건
            {sel ? (
              <>
                {" · "}선택: <strong>{sel.vendorName || sel.supplier}</strong> {won(sel.total)} {sel.lowest ? <Badge tone="ok">최저가</Badge> : <Badge tone="warn">최저가 아님</Badge>}
              </>
            ) : (
              " · 선택한 거래처 없음"
            )}
          </p>
          {sel && !sel.lowest && c.note && <p className="hint">{c.note}</p>}
          {sel && c.reason && (
            <p>
              <strong>선택 사유</strong> {c.reason}
            </p>
          )}
        </>
      )}
      <p>
        <Link to={`/purchase-requests/${prId}/quotes`}>{(editable ?? c.editable) ? "견적 비교·견적서 올리기" : "견적 비교 보기"}</Link>
      </p>
    </aside>
  );
}
