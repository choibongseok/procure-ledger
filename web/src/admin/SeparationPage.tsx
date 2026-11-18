// 권한 분리 점검(/admin/roles/separation, role:manage): 한 사람이 함께 가진 충돌 권한 쌍과 출처 역할. 막지 않고 보여 준다.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { parseAuthError } from "../auth/authApi";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { PERMISSION_LABELS } from "./auditLabels";

interface Finding {
  rule: { a: string; b: string; reason: string };
  count: number;
  users: { userId: number; name: string; rolesA: string[]; rolesB: string[] }[];
}

const label = (c: string) => PERMISSION_LABELS[c] ?? c;

export function SeparationPage() {
  const [findings, setFindings] = useState<Finding[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api
      .get<{ findings: Finding[] }>("/api/admin/roles/separation")
      .then((r) => setFindings(r.findings ?? []))
      .catch((e) => setError(parseAuthError(e).message));
  }, []);
  const hit = (findings ?? []).filter((f) => f.count > 0);
  return (
    <section>
      <h1>권한 분리 점검</h1>
      <p>
        한 사람이 요청·승인·지급처럼 서로 견제해야 할 권한을 함께 가진 경우의 목록입니다. 소규모 회사에서는 불가피할 수 있어 막지는 않고 보여만 줍니다.{" "}
        <Link to="/admin/roles">역할 관리로</Link>
      </p>
      {error && <Notice tone="error">{error}</Notice>}
      {findings && hit.length === 0 && <EmptyState>충돌하는 권한 조합을 가진 사람이 없습니다.</EmptyState>}
      {hit.map((f) => (
        <section key={f.rule.a + f.rule.b} className="card" aria-label={`${label(f.rule.a)} + ${label(f.rule.b)}`}>
          <h2>
            {label(f.rule.a)} + {label(f.rule.b)} <Badge tone="warn">{f.count}명</Badge>
          </h2>
          <p className="hint">{f.rule.reason}</p>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>이름</th>
                  <th>{label(f.rule.a)} 출처 역할</th>
                  <th>{label(f.rule.b)} 출처 역할</th>
                </tr>
              </thead>
              <tbody>
                {f.users.map((u) => (
                  <tr key={u.userId}>
                    <td>{u.name}</td>
                    <td>{u.rolesA.join(", ")}</td>
                    <td>{u.rolesB.join(", ")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </section>
      ))}
    </section>
  );
}
