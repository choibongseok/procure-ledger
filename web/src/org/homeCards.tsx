// (web/src/org). 관리자 홈 카드: "결재·합의자 소멸" 제목 아래 N건(표의 행 수)·없음과 점검 탭 링크(B22 7.2, B23 개정).
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { HomeCard } from "../layout/routeModule";

function OrphanCard() {
  const [n, setN] = useState<number | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    api
      .get<unknown[]>("/api/approvals/orphaned")
      .then((r) => setN(Array.isArray(r) ? r.length : 0))
      .catch(() => setFailed(true));
  }, []);
  if (failed) return <p className="hint">목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.</p>;
  if (n === null) return <p className="hint">불러오는 중…</p>;
  return (
    <div>
      <p>
        <strong>{n === 0 ? "없음" : `${n}건`}</strong>
      </p>
      <p>
        <Link to="/org/manage?tab=check">점검 탭 열기</Link>
      </p>
    </div>
  );
}

export const orgHomeCards: HomeCard[] = [
  { id: "org-orphans", title: "결재·합의자 소멸", permission: "org:manage", order: 30, Component: OrphanCard },
];
