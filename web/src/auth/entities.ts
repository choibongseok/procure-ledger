// 다법인(B44)의 화면 접점 타입: /api/me 가 싣는 법인 정보와 현재 법인 바꾸기. 법인 선택기(GNB)는 erp 가 이 타입으로 붙는다.
import { api } from "../api/client";

/** 현재 법인으로 고를 수 있는 법인(활성). 법인이 하나뿐이면 목록이 한 개라 화면은 선택기를 그리지 않는다. */
export interface EntityChoice {
  id: number;
  code: string;
  name: string;
}

/** 요청이 일하는 현재 법인. all 이면 "전체 법인 조회" 모드(그룹 범위가 있을 때만, 읽기 전용)다. */
export interface CurrentEntity {
  all: boolean;
  id?: number;
  code?: string;
  name?: string;
}

/** /api/me 응답 중 법인 부분. */
export interface MeEntities {
  entities: EntityChoice[];
  currentEntity: CurrentEntity;
  /** 그룹(모든 법인) 범위 권한이 있어 "전체" 모드를 고를 수 있는가. */
  groupScope: boolean;
}

/** 세션의 현재 법인을 바꾼다. code 는 법인 코드이고 "*" 는 전체 모드다. 성공하면 바뀐 법인 정보를 돌려준다(화면은 현재 데이터를 새로 읽는다). */
export function setCurrentEntity(code: string): Promise<MeEntities> {
  return api.put<MeEntities>("/api/me/entity", { entity: code });
}
