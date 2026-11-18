// 현재 법인의 활성 사업장 목록(B44 3단계). 사업장이 하나뿐이면 선택기를 보이지 않는다(화면은 이전과 같다).
import { useEffect, useState } from "react";
import { api } from "../api/client";

export interface SiteChoice {
  id: number;
  siteNo: string;
  name: string;
  kind: string;
  vatMode: string;
}

export function useSites(): SiteChoice[] {
  const [sites, setSites] = useState<SiteChoice[]>([]);
  useEffect(() => {
    let live = true;
    api
      .get<{ sites: SiteChoice[] }>("/api/entity/sites")
      .then((r) => {
        if (live) setSites(r?.sites ?? []);
      })
      .catch(() => {
        if (live) setSites([]);
      });
    return () => {
      live = false;
    };
  }, []);
  return sites;
}

/** 사업장 선택기를 보일지(둘 이상일 때만). */
export const hasSiteChoice = (sites: SiteChoice[]) => sites.length > 1;
