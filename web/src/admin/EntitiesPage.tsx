// 법인 관리 화면(B44 명세 15.1 1항): 법인 목록·등록·수정·비활성, 루트 조직 연결. entity:manage(관리자, 위험 권한·세션 전용).
// 기본 법인의 회사 정보는 회사 설정 화면이 정본이라 여기서는 고치지 않는다. 사업장 탭(EntitySites)은 erp 가 붙였다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, LoadingRows, Notice, PageHead, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { EntitySites } from "./EntitySites";

export interface EntityRow {
  id: number;
  code: string;
  name: string;
  businessNo: string;
  representative: string;
  address: string;
  phone: string;
  fiscalStartMonth: number;
  rootUnitId: number;
  rootUnitName: string;
  isDefault: boolean;
  active: boolean;
  version: number;
  warnings?: string[];
  members: number;
  sites: number;
  updatedAt: string;
}

interface RootCandidate {
  unitId: number;
  name: string;
  code: string;
}

interface Listing {
  entities: EntityRow[];
  rootCandidates: RootCandidate[];
}

interface FormState {
  id: number | null;
  version: number;
  code: string;
  name: string;
  businessNo: string;
  representative: string;
  address: string;
  phone: string;
  fiscalStartMonth: number;
  rootUnitId: number;
}

const emptyForm: FormState = { id: null, version: 0, code: "", name: "", businessNo: "", representative: "", address: "", phone: "", fiscalStartMonth: 1, rootUnitId: 0 };

export function EntitiesPage() {
  const { can } = useAuth();
  const manage = can("entity:manage");
  const data = useLoad(() => (manage ? api.get<Listing>("/api/admin/entities") : Promise.resolve({ entities: [], rootCandidates: [] } as Listing)), [manage]);
  const [tab, setTab] = useState<"entities" | "sites">("entities");
  const [form, setForm] = useState<FormState>(emptyForm);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [warn, setWarn] = useState("");
  const [confirm, setConfirm] = useState<{ entity: EntityRow; on: boolean } | null>(null);
  const list = data.data?.entities ?? [];
  const candidates = data.data?.rootCandidates ?? [];

  async function run(fn: () => Promise<void>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function edit(e: EntityRow) {
    setForm({ id: e.id, version: e.version, code: e.code, name: e.name, businessNo: e.businessNo, representative: e.representative, address: e.address, phone: e.phone, fiscalStartMonth: e.fiscalStartMonth, rootUnitId: 0 });
    setNote(null);
  }

  async function save(ev: FormEvent) {
    ev.preventDefault();
    const body = { code: form.code, name: form.name, businessNo: form.businessNo, representative: form.representative, address: form.address, phone: form.phone, fiscalStartMonth: form.fiscalStartMonth };
    await run(async () => {
      setWarn("");
      if (form.id === null) setWarn(((await api.post<EntityRow>("/api/admin/entities", { ...body, rootUnitId: form.rootUnitId })).warnings ?? []).join(" "));
      else await api.put(`/api/admin/entities/${form.id}`, { ...body, version: form.version });
      setForm(emptyForm);
    }, form.id === null ? "법인을 등록했습니다." : "법인 정보를 저장했습니다.");
  }

  if (!manage) return <Notice tone="error">법인 관리를 볼 권한이 없습니다.</Notice>;

  return (
    <section>
      <PageHead title="법인 관리" />
      <p>사업자번호와 장부가 따로인 회사 단위(법인)를 관리합니다. 각 법인의 업무 데이터는 법인별로 나뉘고, 조직도는 법인마다 최상위 조직(루트)을 둡니다. 기본 법인의 회사 정보는 관리 &gt; 회사 설정에서 바꿉니다.</p>
      <div role="tablist" aria-label="법인 관리" className="form-row">
        {(
          [
            ["entities", "법인"],
            ["sites", "사업장"],
          ] as const
        ).map(([k, label]) => (
          <button key={k} type="button" role="tab" aria-selected={tab === k} className={tab === k ? undefined : "btn-secondary"} onClick={() => setTab(k)}>
            {label}
          </button>
        ))}
      </div>
      {data.error && <Notice tone="error">{data.error}</Notice>}
      {tab === "sites" && <EntitySites entities={list.map((e) => ({ id: e.id, name: e.name, active: e.active }))} />}
      {tab === "entities" && note && <Notice tone={note.tone}>{note.text}</Notice>}
      {tab === "entities" && warn && <Notice tone="warn">{warn}</Notice>}

      {tab === "entities" && (
        <>
      <section className="card" aria-labelledby="ent-list">
        <h2 id="ent-list">법인 목록</h2>
        {data.data && <p className="hint">전체 {list.length}개</p>}
        <TableWrap busy={data.loading}>
          <table className="stack-table" aria-label="법인 목록">
            <thead>
              <tr>
                <th>법인</th>
                <th>코드</th>
                <th>사업자번호</th>
                <th>루트 조직</th>
                <th>조직 수</th>
                <th>사업장</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.loading && !data.data ? <LoadingRows cols={8} /> : null}
              {list.map((e) => (
                <tr key={e.id}>
                  <td data-label="법인">
                    {e.name} {e.isDefault && <Badge tone="info">기본 법인</Badge>}
                  </td>
                  <td data-label="코드">{e.code}</td>
                  <td data-label="사업자번호">{e.businessNo || "-"}</td>
                  <td data-label="루트 조직">
                    {e.isDefault ? (
                      e.rootUnitName || "최상위 조직"
                    ) : (
                      <select
                        aria-label={`${e.name} 루트 조직`}
                        value={e.rootUnitId}
                        disabled={busy || !e.active}
                        onChange={(ev) => void run(async () => void (await api.put(`/api/admin/entities/${e.id}/root`, { rootUnitId: Number(ev.target.value) })), `${e.name} 의 루트 조직을 바꿨습니다.`)}
                      >
                        <option value={0}>연결 없음</option>
                        {e.rootUnitId > 0 && <option value={e.rootUnitId}>{e.rootUnitName}</option>}
                        {candidates.map((c) => (
                          <option key={c.unitId} value={c.unitId}>
                            {c.name}
                          </option>
                        ))}
                      </select>
                    )}
                  </td>
                  <td data-label="조직 수" className="num">
                    {e.members}개
                  </td>
                  <td data-label="사업장" className="num">
                    {e.sites}곳
                  </td>
                  <td data-label="상태">
                    <Badge tone={e.active ? "ok" : "muted"}>{e.active ? "사용 중" : "비활성"}</Badge>
                  </td>
                  <td>
                    {!e.isDefault && (
                      <>
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => edit(e)} aria-label={`${e.name} 수정`}>
                          수정
                        </button>{" "}
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setConfirm({ entity: e, on: !e.active })} aria-label={`${e.name} ${e.active ? "비활성화" : "다시 켜기"}`}>
                          {e.active ? "비활성화" : "다시 켜기"}
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      </section>

      <section className="card" aria-labelledby="ent-form">
        <h2 id="ent-form">{form.id === null ? "법인 등록" : "법인 수정"}</h2>
        <form onSubmit={save} aria-label="법인 양식">
          <div className="form-row">
            <div className="field">
              <label htmlFor="ent-code">법인 코드</label>
              <input id="ent-code" value={form.code} maxLength={6} disabled={form.id !== null} required onChange={(e) => setForm({ ...form, code: e.target.value.toUpperCase() })} />
              <span className="hint">영문 대문자·숫자 2~6자. 문서 번호 앞에 붙고 한 번 정하면 바꿀 수 없습니다.</span>
            </div>
            <div className="field">
              <label htmlFor="ent-name">법인 이름</label>
              <input id="ent-name" value={form.name} maxLength={100} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ent-biz">사업자번호</label>
              <input id="ent-biz" value={form.businessNo} placeholder="000-00-00000" onChange={(e) => setForm({ ...form, businessNo: e.target.value })} />
            </div>
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="ent-rep">대표자</label>
              <input id="ent-rep" value={form.representative} maxLength={50} onChange={(e) => setForm({ ...form, representative: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ent-addr">주소</label>
              <input id="ent-addr" value={form.address} maxLength={200} onChange={(e) => setForm({ ...form, address: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ent-phone">전화</label>
              <input id="ent-phone" value={form.phone} maxLength={30} onChange={(e) => setForm({ ...form, phone: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="ent-fiscal">회계연도 시작월</label>
              <select id="ent-fiscal" value={form.fiscalStartMonth} onChange={(e) => setForm({ ...form, fiscalStartMonth: Number(e.target.value) })}>
                {Array.from({ length: 12 }, (_, i) => i + 1).map((m) => (
                  <option key={m} value={m}>
                    {m}월
                  </option>
                ))}
              </select>
            </div>
          </div>
          {form.id === null && (
            <div className="field">
              <label htmlFor="ent-root">루트 조직</label>
              <select id="ent-root" value={form.rootUnitId} onChange={(e) => setForm({ ...form, rootUnitId: Number(e.target.value) })}>
                <option value={0}>나중에 연결</option>
                {candidates.map((c) => (
                  <option key={c.unitId} value={c.unitId}>
                    {c.name}
                  </option>
                ))}
              </select>
              <span className="hint">조직도에서 먼저 최상위 조직(회사)을 만들면 여기서 고를 수 있습니다. 연결하면 그 조직 아래 모든 조직·사람이 이 법인에 속합니다.</span>
            </div>
          )}
          <p>
            <button type="submit" disabled={busy}>
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

        </>
      )}

      {confirm && (
        <ConfirmDialog
          title={confirm.on ? "법인을 다시 켭니다" : "법인을 비활성화합니다"}
          confirmLabel={confirm.on ? "다시 켜기" : "비활성화"}
          variant={confirm.on ? "primary" : "danger"}
          busy={busy}
          onConfirm={() => {
            const c = confirm;
            setConfirm(null);
            void run(async () => void (await api.post(`/api/admin/entities/${c.entity.id}/${c.on ? "activate" : "deactivate"}`)), c.on ? `${c.entity.name} 을(를) 다시 켰습니다.` : `${c.entity.name} 을(를) 비활성화했습니다.`);
          }}
          onCancel={() => setConfirm(null)}
        >
          <p>{confirm.on ? "이 법인에서 다시 쓰기가 가능해집니다." : "비활성 법인은 읽기만 되고 새로 쓸 수 없습니다. 데이터는 지워지지 않으며 다시 켤 수 있습니다."}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
