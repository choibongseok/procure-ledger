// (web/src/admin). 관리자·감사 홈 카드: 보존 기한이 지난 문서 건수와 보존·폐기 화면 링크(B29 ③). 폐기는 자동으로 일어나지 않으므로 알림은 이 카드뿐이다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { HomeCard } from "../layout/routeModule";
import { STATE_LABEL, type Checklist } from "./OnboardingPage";

function RetentionCard() {
  const [n, setN] = useState<number | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    api
      .get<{ count: number }>("/api/retention/count")
      .then((r) => setN(r.count))
      .catch(() => setFailed(true));
  }, []);
  if (failed) return <p className="hint">건수를 불러오지 못했습니다. 잠시 후 다시 시도하세요.</p>;
  if (n === null) return <p className="hint">불러오는 중…</p>;
  return (
    <div>
      <p>
        <strong>{n === 0 ? "없음" : `${n}건`}</strong>
      </p>
      <p>
        <Link to="/admin/retention">보존·폐기 열기</Link>
      </p>
    </div>
  );
}

/** 도입 점검 요약(B38 명세 5.6): "N/12 완료"와 가장 앞선 미완료 항목 3개 바로가기. 모두 끝났으면 한 줄로 접힌다. */
function OnboardingCard() {
  const [d, setD] = useState<Checklist | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    api
      .get<Checklist>("/api/admin/onboarding")
      .then(setD)
      .catch(() => setFailed(true));
  }, []);
  if (failed) return <p className="hint">도입 점검 현황을 불러오지 못했습니다. 잠시 후 다시 시도하세요.</p>;
  if (d === null) return <p className="hint">불러오는 중…</p>;
  const open = d.items.filter((it) => it.state !== "done" && it.state !== "not_applicable");
  if (open.length === 0) {
    return (
      <p>
        도입 점검을 모두 마쳤습니다. <Link to="/admin/onboarding">도입 점검표 보기</Link>
      </p>
    );
  }
  return (
    <div>
      <p>
        <strong>
          도입 점검: {d.summary.done}/{d.summary.total} 완료
        </strong>
      </p>
      <ul>
        {open.slice(0, 3).map((it) => (
          <li key={it.id}>
            <Link to={it.link || "/admin/onboarding"}>{it.title}</Link> <span className="hint">{STATE_LABEL[it.state]}</span>
          </li>
        ))}
      </ul>
      <p>
        <Link to="/admin/onboarding">도입 점검표 열기</Link>
      </p>
    </div>
  );
}

export const adminHomeCards: HomeCard[] = [
  { id: "onboarding", title: "도입 점검", permission: "onboarding:read", order: 5, Component: OnboardingCard },
  { id: "retention-expired", title: "보존 기한이 지난 문서", permission: "retention:read", order: 40, Component: RetentionCard },
];
