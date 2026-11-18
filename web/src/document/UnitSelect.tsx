// 조직 하나를 고르는 선택 목록. 조직 트리(/api/org/tree)를 들여쓴 목록으로 보인다.
import { useEffect, useState } from "react";
import { api } from "../api/client";

interface Node {
  id: number;
  name: string;
  kind: string;
  closed?: boolean;
  children?: Node[];
}

function flatten(n: Node, depth: number, out: { id: number; label: string }[]) {
  if (n.closed) return;
  out.push({ id: n.id, label: `${"　".repeat(depth)}${n.name}` });
  for (const c of n.children ?? []) flatten(c, depth + 1, out);
}

export function UnitSelect({ id, value, onChange, label }: { id: string; value: number; onChange: (unitId: number, name: string) => void; label?: string }) {
  const [items, setItems] = useState<{ id: number; label: string }[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let stale = false;
    api
      .get<{ root: Node }>("/api/org/tree")
      .then((t) => {
        if (stale) return;
        const out: { id: number; label: string }[] = [];
        flatten(t.root, 0, out);
        setItems(out);
      })
      .catch(() => !stale && setFailed(true));
    return () => {
      stale = true;
    };
  }, []);
  return (
    <>
      <select id={id} aria-label={label} value={value ? String(value) : ""} onChange={(e) => {
          const n = Number(e.target.value) || 0;
          onChange(n, (items ?? []).find((u) => u.id === n)?.label.replace(/^　+/, "") ?? "");
        }}>
        <option value="">조직을 선택하세요</option>
        {(items ?? []).map((u) => (
          <option key={u.id} value={u.id}>
            {u.label}
          </option>
        ))}
      </select>
      {failed && <span className="hint">조직 목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.</span>}
    </>
  );
}
