package scim

import (
	"net/http"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// registerSCIM 은 /scim/v2 엔드포인트를 등록한다. 모든 라우트가 scim:provision 을 선언하고(인증은 Wrap 이 SCIM 토큰으로 한다) 핸들러 안에 권한·역할 분기는 없다.
func (s *Service) registerSCIM(r contract.Registrar) {
	p := contract.PermSCIMProvision
	r.HandleFunc("GET", "/scim/v2/ServiceProviderConfig", p, s.serviceProviderConfig)
	r.HandleFunc("GET", "/scim/v2/Schemas", p, s.schemas)
	r.HandleFunc("GET", "/scim/v2/Schemas/{id}", p, s.schemaByID)
	r.HandleFunc("GET", "/scim/v2/ResourceTypes", p, s.resourceTypes)
	r.HandleFunc("GET", "/scim/v2/ResourceTypes/{id}", p, s.resourceTypeByID)
	r.HandleFunc("GET", "/scim/v2/Users", p, s.listUsers)
	r.HandleFunc("GET", "/scim/v2/Users/{id}", p, s.getUser)
	r.HandleFunc("POST", "/scim/v2/Users", p, s.createUser)
	r.HandleFunc("PUT", "/scim/v2/Users/{id}", p, s.replaceUser)
	r.HandleFunc("PATCH", "/scim/v2/Users/{id}", p, s.patchUser)
	r.HandleFunc("DELETE", "/scim/v2/Users/{id}", p, s.deleteUser)
	// 그룹 연동(B40 3단계): 설정의 연동 방식이 off 이면 목록은 비어 있고 쓰기는 403 이다.
	r.HandleFunc("GET", "/scim/v2/Groups", p, s.listGroups)
	r.HandleFunc("GET", "/scim/v2/Groups/{id}", p, s.getGroup)
	r.HandleFunc("POST", "/scim/v2/Groups", p, s.createGroup)
	r.HandleFunc("PUT", "/scim/v2/Groups/{id}", p, s.replaceGroup)
	r.HandleFunc("PATCH", "/scim/v2/Groups/{id}", p, s.patchGroup)
	r.HandleFunc("DELETE", "/scim/v2/Groups/{id}", p, s.deleteGroup)
}

func (s *Service) serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	writeSCIM(w, http.StatusOK, map[string]any{
		"schemas":          []string{schemaSPC},
		"documentationUri": s.baseURL + "/docs/auth-providers.md",
		"patch":            map[string]any{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": maxCount},
		"changePassword":   map[string]any{"supported": false},
		"sort":             map[string]any{"supported": false},
		"etag":             map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "SCIM 연동 토큰", "description": "관리자 화면에서 발급한 pls_ 토큰을 Authorization: Bearer 로 보냅니다.",
			"specUri": "https://www.rfc-editor.org/info/rfc6750", "primary": true}},
		"meta": map[string]any{"resourceType": "ServiceProviderConfig", "location": s.baseURL + "/scim/v2/ServiceProviderConfig",
			"description": "PUT 에서 보내지 않은 속성은 지우지 않고 변경 없음으로 처리합니다."},
	})
}

func attr(name, typ string, required bool, mutability string) map[string]any {
	return map[string]any{"name": name, "type": typ, "multiValued": false, "required": required, "caseExact": false, "mutability": mutability, "returned": "default", "uniqueness": "none"}
}

func userSchema() map[string]any {
	un := attr("userName", "string", true, "readWrite")
	un["uniqueness"] = "server"
	return map[string]any{"schemas": []string{schemaSchema}, "id": schemaUser, "name": "User", "description": "사용자(입사·퇴사 발령으로 반영됩니다)",
		"attributes": []map[string]any{un, attr("externalId", "string", false, "readWrite"), attr("displayName", "string", false, "readWrite"),
			attr("title", "string", false, "readWrite"), attr("active", "boolean", false, "readWrite")},
		"meta": map[string]any{"resourceType": "Schema"}}
}

func entSchema() map[string]any {
	return map[string]any{"schemas": []string{schemaSchema}, "id": schemaEnt, "name": "EnterpriseUser", "description": "엔터프라이즈 확장(부서·사번)",
		"attributes": []map[string]any{attr("department", "string", false, "readWrite"), attr("employeeNumber", "string", false, "readWrite")},
		"meta":       map[string]any{"resourceType": "Schema"}}
}

func (s *Service) schemas(w http.ResponseWriter, _ *http.Request) {
	res := []map[string]any{userSchema(), entSchema(), groupSchema()}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{schemaList}, "totalResults": len(res), "startIndex": 1, "itemsPerPage": len(res), "Resources": res})
}

func (s *Service) schemaByID(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("id") {
	case schemaUser:
		writeSCIM(w, http.StatusOK, userSchema())
	case schemaEnt:
		writeSCIM(w, http.StatusOK, entSchema())
	case schemaGroup:
		writeSCIM(w, http.StatusOK, groupSchema())
	default:
		writeAPIError(w, errNotFound("스키마를 찾을 수 없습니다."))
	}
}

func (s *Service) userResourceType() map[string]any {
	return map[string]any{"schemas": []string{schemaRT}, "id": "User", "name": "User", "endpoint": "/Users", "schema": schemaUser,
		"schemaExtensions": []map[string]any{{"schema": schemaEnt, "required": false}},
		"meta":             map[string]any{"resourceType": "ResourceType", "location": s.baseURL + "/scim/v2/ResourceTypes/User"}}
}

func (s *Service) groupResourceType() map[string]any {
	return map[string]any{"schemas": []string{schemaRT}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": schemaGroup,
		"meta": map[string]any{"resourceType": "ResourceType", "location": s.baseURL + "/scim/v2/ResourceTypes/Group"}}
}

func groupSchema() map[string]any {
	return map[string]any{"schemas": []string{schemaSchema}, "id": schemaGroup, "name": "Group", "description": "그룹(관리자가 정한 매핑이 있는 그룹만, 멤버 변경이 전보 또는 역할 부여가 됩니다)",
		"attributes": []map[string]any{attr("displayName", "string", true, "readWrite"), attr("externalId", "string", false, "readWrite"), attr("members", "complex", false, "readWrite")},
		"meta":       map[string]any{"resourceType": "Schema"}}
}

func (s *Service) resourceTypes(w http.ResponseWriter, _ *http.Request) {
	res := []map[string]any{s.userResourceType(), s.groupResourceType()}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{schemaList}, "totalResults": len(res), "startIndex": 1, "itemsPerPage": len(res), "Resources": res})
}

func (s *Service) resourceTypeByID(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("id") {
	case "User":
		writeSCIM(w, http.StatusOK, s.userResourceType())
	case "Group":
		writeSCIM(w, http.StatusOK, s.groupResourceType())
	default:
		writeAPIError(w, errNotFound("리소스 종류를 찾을 수 없습니다."))
	}
}
