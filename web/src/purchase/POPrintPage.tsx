// 발주서 출력 화면(B25 ③): 브라우저 인쇄용 A4 한 장. 결재란은 결재 문서 출력 화면과 같은 컴포넌트다.
// 부가세(발주일의 부가세율, 원 미만 버림)는 서버가 계산한다. 사업자번호·연락처는 mask:bypass 역할(재무·관리자)만 전체가 보인다.
// 인쇄 버튼을 누르면 출력 사실을 감사에 남기고(po.print) 브라우저 인쇄를 연다. 편집 기능은 없다.
import { useState } from "react";
import { useParams } from "react-router-dom";
import { api } from "../api/client";
import { PrintStyle, SignTable } from "../document/PrintView";
import { Breadcrumbs, Notice } from "../layout/ui";
import { dateTime, won } from "./labels";
import type { PurchaseRequest } from "./types";
import { errorText, useLoad } from "./useLoad";

interface POPrint {
  prId: number;
  prNo: string;
  poNo: string;
  poDate: string;
  company: string;
  companyBizNo?: string;
  companyRep?: string;
  companyAddr?: string;
  companyPhone?: string;
  hasLogo?: boolean;
  hasSeal?: boolean;
  department: string;
  requester: string;
  supplierName: string;
  businessNo: string;
  phone: string;
  email: string;
  contactEmpty: boolean;
  masked: boolean;
  deliveryPlace: string;
  dueDate: string;
  lines: { no: number; itemName: string; unit: string; quantity: number; unitPrice: number; amount: number }[];
  supply: number;
  vatRate?: number;
  vat: number;
  total: number;
  note: string;
}

/** 서버 오류 문구("현재 상태에서는 …: 안내")에서 사람에게 보일 안내만 꺼낸다. */
function guide(msg: string): string {
  const i = msg.indexOf(": ");
  return i >= 0 ? msg.slice(i + 2) : msg;
}

export function POPrintPage() {
  const { id } = useParams();
  const po = useLoad(() => api.get<POPrint>(`/api/purchase-requests/${id}/po`), [id]);
  const pr = useLoad(() => api.get<PurchaseRequest>(`/api/purchase-requests/${id}`), [id]);
  const [err, setErr] = useState("");

  async function print() {
    setErr("");
    try {
      await api.post(`/api/purchase-requests/${id}/po/print`);
      window.print();
    } catch (e) {
      setErr(errorText(e));
    }
  }

  const back = `/purchase-requests/${id}`;
  if (po.error)
    return (
      <section>
        <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: "구매요청 상세", to: back }, { label: "발주서" }]} />
        <h1>발주서</h1>
        <Notice tone="warn">{guide(po.error)}</Notice>
      </section>
    );
  if (!po.data || !pr.data) return <p>불러오는 중…</p>;
  const d = po.data;

  return (
    <section className="print-page" aria-label="발주서 출력 화면">
      <PrintStyle />
      <div className="no-print" style={{ display: "flex", alignItems: "center", gap: 16, flexWrap: "wrap" }}>
        <button type="button" onClick={() => void print()}>
          인쇄
        </button>
        <span className="hint">인쇄 창에서 “PDF 로 저장”을 고르면 파일로 저장됩니다.</span>
        <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: pr.data.no, to: back }, { label: "발주서" }]} />
        {err && <Notice tone="error">{err}</Notice>}
      </div>
      {d.masked && (
        <div className="no-print">
          <Notice tone="info">사업자번호와 연락처는 일부만 보입니다. 전체 값은 재무·관리자에게만 보입니다.</Notice>
        </div>
      )}
      {d.contactEmpty && (
        <div className="no-print">
          <Notice tone="warn">거래처 연락처가 비어 있어 발주서의 연락처 칸이 비어 있습니다. 거래처 정보를 먼저 채우세요.</Notice>
        </div>
      )}
      {d.hasLogo && <img src="/api/company/logo" alt={`${d.company} 로고`} style={{ maxHeight: 48, maxWidth: 200 }} />}
      <h1 style={{ textAlign: "center" }}>발 주 서</h1>
      <SignTable d={pr.data} />
      <table aria-label="발주 정보" style={{ marginTop: 14 }}>
        <tbody>
          <tr>
            <th>발주 번호</th>
            <td>{d.poNo}</td>
            <th>발주일</th>
            <td>{d.poDate ? dateTime(d.poDate) : "—"}</td>
          </tr>
          <tr>
            <th>구매요청 번호</th>
            <td>{d.prNo}</td>
            <th>납기</th>
            <td>{d.dueDate || "협의"}</td>
          </tr>
          <tr>
            <th>발주처</th>
            <td>
              {d.company || "—"}
              {d.department ? ` ${d.department}` : ""}
              {(d.companyBizNo || d.companyRep || d.companyAddr || d.companyPhone) && (
                <div className="hint" aria-label="발주처 정보">
                  {[d.companyBizNo && `사업자번호 ${d.companyBizNo}`, d.companyRep && `대표 ${d.companyRep}`, d.companyAddr, d.companyPhone].filter(Boolean).join(" · ")}
                </div>
              )}
              {d.hasSeal && <img src="/api/company/seal" alt={`${d.company} 직인`} style={{ maxHeight: 64, maxWidth: 64, display: "block" }} />}
            </td>
            <th>담당자</th>
            <td>{d.requester}</td>
          </tr>
          <tr>
            <th>납품처</th>
            <td colSpan={3}>{d.deliveryPlace}</td>
          </tr>
        </tbody>
      </table>
      <table aria-label="공급자" style={{ marginTop: 14 }}>
        <tbody>
          <tr>
            <th>공급자</th>
            <td>{d.supplierName || "—"}</td>
            <th>사업자번호</th>
            <td>{d.businessNo || "—"}</td>
          </tr>
          <tr>
            <th>연락처</th>
            <td>{d.phone || "—"}</td>
            <th>이메일</th>
            <td>{d.email || "—"}</td>
          </tr>
        </tbody>
      </table>
      <table aria-label="품목" style={{ marginTop: 14 }}>
        <thead>
          <tr>
            <th>번호</th>
            <th>품목</th>
            <th>단위</th>
            <th>수량</th>
            <th>단가</th>
            <th>금액</th>
          </tr>
        </thead>
        <tbody>
          {d.lines.map((l) => (
            <tr key={l.no}>
              <td>{l.no}</td>
              <td>{l.itemName}</td>
              <td>{l.unit}</td>
              <td style={{ textAlign: "right" }}>{l.quantity.toLocaleString("ko-KR")}</td>
              <td style={{ textAlign: "right" }}>{won(l.unitPrice)}</td>
              <td style={{ textAlign: "right" }}>{won(l.amount)}</td>
            </tr>
          ))}
          <tr>
            <th colSpan={5}>공급가액</th>
            <td style={{ textAlign: "right" }}>{won(d.supply)}</td>
          </tr>
          <tr>
            <th colSpan={5}>부가세({d.vatRate ?? 10}%)</th>
            <td style={{ textAlign: "right" }}>{won(d.vat)}</td>
          </tr>
          <tr>
            <th colSpan={5}>합계</th>
            <td style={{ textAlign: "right" }}>
              <strong>{won(d.total)}</strong>
            </td>
          </tr>
        </tbody>
      </table>
      <h2>비고</h2>
      <p>{d.note}</p>
    </section>
  );
}
