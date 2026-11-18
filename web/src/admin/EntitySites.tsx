// 법인 관리 화면의 사업장 탭(B44 명세 15.1 1항): 법인마다 주사업장·지점·공장, 종사업장번호, 총괄납부 여부. API 는 /api/admin/entities/{id}/sites(entity:manage, 위험 권한·세션 전용).
// 사업장 종류·신고 방식 이름은 서버가 한국어로 함께 준다(내부 코드를 화면에 보이지 않는다).
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, EmptyState, LoadingRows, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export interface SiteRow {
  id: number;
  entityId: number;
  siteNo: string;
  name: string;
  kind: "head" | "branch" | "factory";
  kindName: string;
  address: string;
  vatMode: "own" | "consolidated";
  vatName: string;
  active: boolean;
  updatedAt: string;
}

interface EntityOption {
  id: number;
  name: string;
  active: boolean;
}

interface FormState {
  id: number | null;
  siteNo: string;
  name: string;
  kind: SiteRow["kind"];
  address: string;
  vatMode: SiteRow["vatMode"];
  active: boolean;
}

const emptyForm: FormState = { id: null, siteNo: "", name: "", kind: "branch", address: "", vatMode: "own", active: true };

const KIND_CHOICES: [SiteRow["kind"], string][] = [
  ["head", "주사업장"],
  ["branch", "지점"],
  ["factory", "공장"],
];
const VAT_CHOICES: [SiteRow["vatMode"], string][] = [
  ["own", "사업장별 신고"],
  ["consolidated", "주사업장 총괄납부"],
];

export function EntitySites({ entities }: { entities: EntityOption[] }) {
  const [picked, setPicked] = useState<number | null>(null);
  // 고른 법인이 없으면 목록의 첫 법인을 보인다(효과로 채우지 않고 파생한다).
  const entityId = picked ?? entities[0]?.id ?? 0;
  const entity = entities.find((e) => e.id === entityId);
  const sites = useLoad(() => (entityId > 0 ? api.get<SiteRow[]>(`/api/admin/entities/${entityId}/sites`) : Promise.resolve([] as SiteRow[])), [entityId]);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const list = sites.data ?? [];
  const hasHead = list.some((s) => s.kind === "head");

  async function run(fn: () => Promise<void>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await sites.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function edit(s: SiteRow) {
    setForm({ id: s.id, siteNo: s.siteNo, name: s.name, kind: s.kind, address: s.address, vatMode: s.vatMode, active: s.active });
    setNote(null);
  }

  async function save(ev: FormEvent) {
    ev.preventDefault();
    const body = { siteNo: form.siteNo, name: form.name, kind: form.kind, address: form.address, vatMode: form.vatMode, active: form.active };
    await run(async () => {
      if (form.id === null) await api.post(`/api/admin/entities/${entityId}/sites`, body);
      else await api.put(`/api/admin/entities/${entityId}/sites/${form.id}`, body);
      setForm(emptyForm);
    }, form.id === null ? "사업장을 등록했습니다." : "사업장 정보를 저장했습니다.");
  }

  // 주사업장은 법인마다 하나이고 종류를 바꿀 수 없으며 종사업장번호는 0000 이다. 그 밖의 사업장은 0000 을 쓸 수 없다.
  const headForm = form.kind === "head";

  return (
    <div role="tabpanel" aria-label="사업장">
      <p>법인마다 사업장을 둘 수 있습니다. 주사업장은 법인마다 하나이고 종사업장번호는 0000 입니다. 부가세는 사업장별로 신고하거나 주사업장에서 총괄해서 납부합니다.</p>
      <div className="field">
        <label htmlFor="site-entity">법인</label>
        <select
          id="site-entity"
          value={entityId}
          onChange={(e) => {
            setPicked(Number(e.target.value));
            setForm(emptyForm);
            setNote(null);
          }}
        >
          {entities.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
              {e.active ? "" : " (비활성)"}
            </option>
          ))}
        </select>
      </div>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {sites.error && <Notice tone="error">{sites.error}</Notice>}

      <section className="card" aria-labelledby="site-list">
        <h2 id="site-list">사업장 목록</h2>
        <TableWrap busy={sites.loading}>
          <table className="stack-table" aria-label="사업장 목록">
            <thead>
              <tr>
                <th>종사업장번호</th>
                <th>사업장</th>
                <th>종류</th>
                <th>주소</th>
                <th>부가세 신고</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {sites.loading && !sites.data ? <LoadingRows cols={7} /> : null}
              {list.map((s) => (
                <tr key={s.id}>
                  <td data-label="종사업장번호">{s.siteNo}</td>
                  <td data-label="사업장">{s.name}</td>
                  <td data-label="종류">{s.kind === "head" ? <Badge tone="info">{s.kindName}</Badge> : s.kindName}</td>
                  <td data-label="주소">{s.address || "-"}</td>
                  <td data-label="부가세 신고">{s.vatName}</td>
                  <td data-label="상태">
                    <Badge tone={s.active ? "ok" : "muted"}>{s.active ? "사용 중" : "사용 안 함"}</Badge>
                  </td>
                  <td>
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => edit(s)} aria-label={`${s.name} 수정`}>
                      수정
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
        {sites.data && list.length === 0 && <EmptyState>등록된 사업장이 없습니다.</EmptyState>}
      </section>

      <section className="card" aria-labelledby="site-form">
        <h2 id="site-form">{form.id === null ? "사업장 등록" : "사업장 수정"}</h2>
        <form onSubmit={save} aria-label="사업장 양식">
          <div className="form-row">
            <div className="field">
              <label htmlFor="site-kind">종류</label>
              <select
                id="site-kind"
                value={form.kind}
                disabled={form.id !== null}
                onChange={(e) => {
                  const kind = e.target.value as SiteRow["kind"];
                  setForm({ ...form, kind, siteNo: kind === "head" ? "0000" : form.siteNo === "0000" ? "" : form.siteNo });
                }}
              >
                {KIND_CHOICES.filter(([k]) => k !== "head" || !hasHead || form.kind === "head").map(([k, label]) => (
                  <option key={k} value={k}>
                    {label}
                  </option>
                ))}
              </select>
              {form.id !== null && <span className="hint">등록한 사업장의 종류는 바꿀 수 없습니다.</span>}
            </div>
            <div className="field">
              <label htmlFor="site-no">종사업장번호</label>
              <input id="site-no" inputMode="numeric" maxLength={4} value={headForm ? "0000" : form.siteNo} disabled={headForm} required onChange={(e) => setForm({ ...form, siteNo: e.target.value.replace(/\D/g, "") })} />
              <span className="hint">숫자 4자리. 주사업장은 0000 이고 다른 사업장은 0000 을 쓸 수 없습니다.</span>
            </div>
            <div className="field">
              <label htmlFor="site-name">사업장 이름</label>
              <input id="site-name" value={form.name} maxLength={60} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="site-addr">주소</label>
              <input id="site-addr" value={form.address} maxLength={200} onChange={(e) => setForm({ ...form, address: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="site-vat">부가세 신고 방식</label>
              <select id="site-vat" value={form.vatMode} onChange={(e) => setForm({ ...form, vatMode: e.target.value as SiteRow["vatMode"] })}>
                {VAT_CHOICES.map(([k, label]) => (
                  <option key={k} value={k}>
                    {label}
                  </option>
                ))}
              </select>
            </div>
            {form.id !== null && (
              <div className="field">
                <label htmlFor="site-active">상태</label>
                <select id="site-active" value={form.active ? "on" : "off"} disabled={form.kind === "head"} onChange={(e) => setForm({ ...form, active: e.target.value === "on" })}>
                  <option value="on">사용 중</option>
                  <option value="off">사용 안 함</option>
                </select>
              </div>
            )}
          </div>
          <p>
            <button type="submit" disabled={busy || !entity}>
              {form.id === null ? "등록" : "저장"}
            </button>{" "}
            {form.id !== null && (
              <button type="button" className="btn-secondary" onClick={() => setForm(emptyForm)}>
                수정 취소
              </button>
            )}
          </p>
        </form>
      </section>
    </div>
  );
}
