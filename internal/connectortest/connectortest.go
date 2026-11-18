// Package connectortest 는 커넥터(internal/connector) 시험용 모의 외부 REST 수신 서버다(B42 3단계).
// 받은 요청(경로·인증 헤더·본문)을 기록하고, 응답 상태를 시험이 정한다. 운영 코드가 쓰지 않는다.
package connectortest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Request 는 받은 요청 하나다.
type Request struct {
	Path string
	Auth string
	Rows []map[string]string
}

// Receiver 는 모의 수신 서버다.
type Receiver struct {
	Server *httptest.Server

	mu     sync.Mutex
	reqs   []Request
	status int
}

// New 는 서버를 띄운다. 호출한 시험이 Close 를 부른다.
func New() *Receiver {
	r := &Receiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var body struct {
			Rows []map[string]string `json:"rows"`
		}
		_ = json.Unmarshal(b, &body)
		r.mu.Lock()
		r.reqs = append(r.reqs, Request{Path: req.URL.Path, Auth: req.Header.Get("Authorization"), Rows: body.Rows})
		st := r.status
		r.mu.Unlock()
		w.WriteHeader(st)
	}))
	return r
}

// Close 는 서버를 닫는다.
func (r *Receiver) Close() { r.Server.Close() }

// URL 은 서버 주소(http, 루프백)다.
func (r *Receiver) URL() string { return r.Server.URL }

// SetStatus 는 이후 응답의 상태 코드를 정한다.
func (r *Receiver) SetStatus(code int) {
	r.mu.Lock()
	r.status = code
	r.mu.Unlock()
}

// Requests 는 받은 요청의 사본이다.
func (r *Receiver) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.reqs...)
}
