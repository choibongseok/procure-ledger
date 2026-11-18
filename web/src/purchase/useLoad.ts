// 화면 공용 조회 훅과 오류 문구.
import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";

/** 서버 오류의 분류 머리말(코드 성격의 문구). 업무 담당자에게는 사유와 다음 할 일만 보인다. */
const SERVER_PREFIXES = ["입력값이 올바르지 않습니다: ", "현재 상태에서는 할 수 없는 동작입니다: ", "권한이 없습니다: "];

/** 사유만 있는 서버 문구에 다음 할 일을 덧붙인다(브랜드 북: 무엇이 안 됐는지 + 다음 할 일). */
const NEXT_STEP: [RegExp, string][] = [
  [/^본인이 올린 요청은 결재할 수 없습니다$/, "본인이 올린 요청은 결재할 수 없습니다. 결재선의 다른 결재자에게 요청하거나 관리자에게 결재자 변경을 요청하세요."],
  [
    /^같은 구매요청의 다른 결재 단계를 이미 결재하셨습니다/,
    "같은 구매요청의 다른 결재 단계를 이미 결재하셨습니다. 한 사람이 두 단계를 결재할 수 없으니 다음 결재자의 처리를 기다리세요.",
  ],
  [/^결재할 수 있는 결재자가 있어 회수할 수 없습니다$/, "회수하지 못했습니다. 그사이 결재자가 정해져 결재가 다시 진행 중입니다. 화면을 새로 고쳐 현재 결재자를 확인하세요."],
  [/^지금은 결재 차례가 아닙니다$/, "지금은 결재 차례가 아닙니다. 결재함에서 내 차례인 건을 확인하세요."],
  [/^반려 사유를 입력해야 합니다$/, "반려 사유를 입력해야 합니다. 사유를 적은 뒤 다시 반려하세요."],
  [/^결재할 수 있는 상태가 아닙니다/, "이미 처리되었거나 결재할 수 없는 상태입니다. 화면을 새로 고쳐 현재 상태를 확인하세요."],
];

/** 서버 오류 문구에서 머리말을 떼고 다음 할 일을 붙인다. */
export function businessMessage(raw: string): string {
  let msg = raw.trim();
  for (const pre of SERVER_PREFIXES) {
    if (msg.startsWith(pre)) {
      msg = msg.slice(pre.length);
      break;
    }
  }
  for (const [re, text] of NEXT_STEP) if (re.test(msg)) return text;
  return msg;
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.message ? businessMessage(e.message) : "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
  return "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
}

/** fn 을 deps 가 바뀔 때마다 다시 불러온다. reload 로 수동 갱신. */
export function useLoad<T>(fn: () => Promise<T>, deps: readonly unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(fn, deps);
  const reload = useCallback(async () => {
    setLoading(true);
    try {
      setData(await run());
      setError("");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [run]);
  useEffect(() => {
    void reload();
  }, [reload]);
  return { data, error, loading, reload };
}
