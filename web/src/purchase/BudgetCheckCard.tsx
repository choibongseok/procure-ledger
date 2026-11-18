// 구매요청 작성 화면의 "예산 확인" 카드(B25 ①). 상신 때 서버가 하는 판정(judgeBudget)과 같은 계산을 미리 보여 준다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Badge, Notice } from "../layout/ui";
import { won } from "./labels";
import type { BudgetCheck, BudgetSnapshot } from "./types";

const DEBOUNCE_MS = 400;

/** "2026-11" → "2026년 11월". */
export function monthText(m: string | undefined): string {
  const [y, mo] = (m ?? "").split("-");
  return y && mo ? `${y}년 ${Number(mo)}월` : (m ?? "");
}

/** 상세·목록·결재함에 보이는 "예산 초과" 배지(상신 때 초과로 판정된 건만). */
export function BudgetOverBadge({ budget }: { budget?: BudgetSnapshot }) {
  if (!budget || budget.state !== "over") return null;
  return <Badge tone="bad">예산 초과 요청</Badge>;
}

export function BudgetCheckCard({ amount }: { amount: number }) {
  const [check, setCheck] = useState<BudgetCheck | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!(amount > 0)) {
      setCheck(null);
      setFailed(false);
      return;
    }
    let stale = false;
    const timer = setTimeout(() => {
      api
        .get<BudgetCheck>(`/api/budgets/check?amount=${Math.round(amount)}`)
        .then((c) => {
          if (stale) return;
          setCheck(c);
          setFailed(false);
        })
        .catch(() => {
          if (stale) return;
          setCheck(null);
          setFailed(true);
        });
    }, DEBOUNCE_MS);
    return () => {
      stale = true;
      clearTimeout(timer);
    };
  }, [amount]);

  return (
    <aside className="card" aria-label="예산 확인">
      <h2>예산 확인</h2>
      {!(amount > 0) && <p>금액을 입력하면 부서 예산을 확인합니다.</p>}
      {failed && <Notice tone="warn">예산을 불러오지 못했습니다. 잠시 뒤 다시 시도해 주세요. 상신 때 서버가 다시 확인합니다.</Notice>}
      {check && amount > 0 && <CheckBody c={check} />}
    </aside>
  );
}

function CheckBody({ c }: { c: BudgetCheck }) {
  if (c.state === "unregistered") {
    return (
      <p>
        {c.departmentName ? `${c.departmentName}의 ` : ""}
        {monthText(c.month)} 예산이 등록되지 않아 예산 확인을 하지 않습니다.
      </p>
    );
  }
  return (
    <>
      <p className="hint">
        {c.departmentName} {monthText(c.month)} 예산입니다. 잔액은 월 예산에서 사용액과 결재 진행 중 금액을 뺀 값입니다(공급가액 기준).
      </p>
      <dl>
        <dt>월 예산</dt>
        <dd>{won(c.budget)}</dd>
        <dt>사용액</dt>
        <dd>{won(c.used)}</dd>
        <dt>결재 진행 중</dt>
        <dd>{won(c.inProgress)}</dd>
        <dt>잔액</dt>
        <dd>{won(c.balance)}</dd>
        <dt>이 요청</dt>
        <dd>{won(c.amount)}</dd>
        <dt>요청 뒤 잔액</dt>
        <dd>{won(c.after)}</dd>
      </dl>
      {c.state === "over" ? (
        <Notice tone="warn">
          <strong>예산을 {won(c.over)} 넘습니다.</strong> 상신하면 결재선에 재무 단계가 더해집니다. 결재선 미리보기에서 확인하세요.
        </Notice>
      ) : (
        <p>
          <Badge tone="ok">예산 이내</Badge>
        </p>
      )}
    </>
  );
}
