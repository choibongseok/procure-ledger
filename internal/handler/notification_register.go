// 내 앱 안 알림 라우트(B21 3.9). 권한은 전부 notification:read 이고 서비스가 호출자 본인 것만 돌려준다.
package handler

import (
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterNotification 은 /api/notifications... 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterNotification(r contract.Registrar, d deps.Deps) {
	if d.Notification == nil {
		return
	}
	h := notificationHandler{svc: d.Notification}
	r.HandleFunc("GET", "/api/notifications", contract.PermNotificationRead, h.list)
	r.HandleFunc("GET", "/api/notifications/unread-count", contract.PermNotificationRead, h.unreadCount)
	r.HandleFunc("POST", "/api/notifications/read-all", contract.PermNotificationRead, h.readAll)
	r.HandleFunc("POST", "/api/notifications/{id}/read", contract.PermNotificationRead, h.read)
}

type notificationHandler struct{ svc domain.NotificationService }

type notificationDTO struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Link      string  `json:"link"`
	CreatedAt string  `json:"createdAt"`
	ReadAt    *string `json:"readAt"`
}

func (h notificationHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.NotificationFilter{UnreadOnly: q.Get("unread") == "1" || q.Get("unread") == "true"}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "limit 이 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		f.Limit = n
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "before 가 올바르지 않습니다.", http.StatusBadRequest)
			return
		}
		f.BeforeID = n
	}
	list, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]notificationDTO, len(list))
	for i, n := range list {
		out[i] = notificationDTO{ID: n.ID, Title: n.Title, Body: n.Body, Link: n.Link, CreatedAt: ts(n.CreatedAt)}
		if n.ReadAt != nil {
			s := ts(*n.ReadAt)
			out[i].ReadAt = &s
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h notificationHandler) unreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.UnreadCount(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
}

func (h notificationHandler) read(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.MarkRead(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h notificationHandler) readAll(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.MarkAllRead(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"marked": n})
}
