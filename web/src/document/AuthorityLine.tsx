// 전결 근거 한 줄(B23 단계 3, 명세 3.13). 결재 상세·결재선 미리보기·출력 화면이 같이 쓴다.
// "전결 근거: 제2조" 를 누르면 규정 문서의 조항 원문이 열린다(규정 검색 서비스). 볼 수 없으면 한국어 안내를 보인다.
import { useState } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import type { AuthorityNote } from "../purchase/types";
import { errorText } from "../purchase/useLoad";

interface Clause {
  docTitle: string;
  clauseNo: string;
  heading: string;
  source: string;
  text: string;
}

export function AuthorityLine({ note, plain, hideWarning }: { note?: AuthorityNote | null; plain?: boolean; hideWarning?: boolean }) {
  const [clause, setClause] = useState<Clause | null>(null);
  const [err, setErr] = useState("");
  if (!note) return null;
  if (note.warning) return hideWarning ? null : <Notice tone="warn">{note.warning}</Notice>;
  const hasClause = !!note.slug && !!note.clauseNo;
  async function open() {
    setErr("");
    try {
      setClause(await api.get<Clause>(`/api/authority-clause?slug=${encodeURIComponent(note!.slug)}&no=${encodeURIComponent(note!.clauseNo)}`));
    } catch {
      setErr("근거 조항을 볼 수 없습니다.");
    }
  }
  return (
    <div>
      <p>
        전결: {note.finalTitle} 결재로 결재가 끝납니다.
        {hasClause && (
          <>
            {" "}
            전결 근거:{" "}
            {plain ? (
              note.clauseNo
            ) : (
              <button type="button" className="btn-link" onClick={() => void open()}>
                {note.clauseNo}
              </button>
            )}
          </>
        )}
      </p>
      {err && <Notice tone="warn">{err}</Notice>}
      {clause && (
        <blockquote>
          <strong>{clause.source || `${clause.docTitle} ${clause.clauseNo}`}</strong>
          <p>{clause.text}</p>
        </blockquote>
      )}
    </div>
  );
}

export function authorityErr(e: unknown): string {
  return errorText(e);
}
