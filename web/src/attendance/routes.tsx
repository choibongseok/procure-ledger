// (web/src/attendance). 근태·휴가(B50) 화면 모듈. 메뉴는 모듈이 켜져 있을 때만 보인다(module: "attendance").
import { guarded } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { AttendanceCalendarPage } from "./AttendanceCalendarPage";
import { AttendanceAdminPage } from "./AttendanceAdminPage";
import { AttendancePage } from "./AttendancePage";
import { AttendanceSettingsPage } from "./AttendanceSettingsPage";
import { AttendanceTeamPage } from "./AttendanceTeamPage";
import { attendanceHomeCards } from "./homeCards";
import { MyLeavePage } from "./MyLeavePage";

export const attendanceModule: RouteModule = {
  name: "attendance",
  homeCards: attendanceHomeCards,
  routes: [
    { path: "/attendance", element: guarded("attendance:self", <AttendancePage />) },
    { path: "/attendance/calendar", element: guarded("attendance:self", <AttendanceCalendarPage />) },
    { path: "/attendance/team", element: guarded("attendance:self", <AttendanceTeamPage />) },
    { path: "/attendance/admin", element: guarded("attendance:hr", <AttendanceAdminPage />) },
    { path: "/attendance/leave", element: guarded("attendance:self", <MyLeavePage />) },
    { path: "/admin/attendance", element: guarded("attendance:policy", <AttendanceSettingsPage />) },
  ],
  menu: [
    { label: "내 근태", path: "/attendance", permission: "attendance:self", module: "attendance", order: 55.5, group: "org" },
    { label: "내 휴가", path: "/attendance/leave", permission: "attendance:self", module: "attendance", order: 56, group: "org" },
    { label: "부서 달력", path: "/attendance/calendar", permission: "attendance:self", module: "attendance", order: 56.2, group: "org" },
    { label: "팀 근태 현황", path: "/attendance/team", permission: "attendance:team", anyOf: ["attendance:team", "attendance:hr"], module: "attendance", order: 56.4, group: "org" },
    { label: "근태 관리", path: "/attendance/admin", permission: "attendance:hr", module: "attendance", order: 57, group: "org" },
    { label: "근태·휴가 설정", path: "/admin/attendance", permission: "attendance:policy", order: 88.792, group: "admin" },
  ],
};
