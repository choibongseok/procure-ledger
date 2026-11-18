// (web/src/org). 조직 트리: 펼침·접기, 인원 수, 조직장, 폐지 표시.
import { useState } from "react";
import { Badge } from "../layout/ui";
import { KIND_LABEL, type TreeNode } from "./types";

interface Props {
  root: TreeNode;
  selectedId: number | null;
  onSelect: (node: TreeNode) => void;
  /** 처음부터 펼쳐 둘 깊이(회사가 0). */
  openDepth?: number;
  showCounts?: boolean;
}

export function OrgTree({ root, selectedId, onSelect, openDepth = 2, showCounts = true }: Props) {
  // 사용자가 바꾼 펼침 상태만 저장한다(없으면 깊이 기준 기본값).
  const [toggled, setToggled] = useState<Record<number, boolean>>({});
  const isOpen = (n: TreeNode, depth: number) => (n.id in toggled ? toggled[n.id] : depth < openDepth);

  function renderNode(n: TreeNode, depth: number) {
    const open = isOpen(n, depth);
    const hasKids = n.children.length > 0;
    return (
      <li key={n.id} role="treeitem" aria-expanded={hasKids ? open : undefined} aria-selected={n.id === selectedId} className="org-node">
        <div className={n.id === selectedId ? "org-row org-row-selected" : "org-row"}>
          {hasKids ? (
            <button
              type="button"
              className="btn-secondary btn-sm org-toggle"
              aria-label={`${n.name} ${open ? "접기" : "펼치기"}`}
              onClick={() => setToggled((t) => ({ ...t, [n.id]: !open }))}
            >
              {open ? "−" : "+"}
            </button>
          ) : (
            <span className="org-toggle-gap" />
          )}
          <button type="button" className="org-select" onClick={() => onSelect(n)}>
            <span className="org-name">{n.name}</span> <span className="hint org-meta">{KIND_LABEL[n.kind]}</span>
            {n.closed && (
              <>
                {" "}
                <Badge tone="muted">폐지</Badge>
              </>
            )}
            {n.headName && (
              <>
                {" "}
                <span className="hint org-meta">
                  · {n.headName}
                  {n.headActing ? "(대행)" : ""}
                </span>
              </>
            )}
            {!n.headName && !n.closed && n.kind !== "company" && (
              <>
                {" "}
                <span className="hint org-meta">· 공석</span>
              </>
            )}
            {showCounts && (
              <>
                {" "}
                <span className="hint org-meta">
                  · 직속 {n.direct}명 / 전체 {n.total}명
                </span>
              </>
            )}
          </button>
        </div>
        {hasKids && open && <ul role="group">{n.children.map((c) => renderNode(c, depth + 1))}</ul>}
      </li>
    );
  }

  return (
    <ul role="tree" aria-label="조직 트리" className="org-tree">
      {renderNode(root, 0)}
    </ul>
  );
}
