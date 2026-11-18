package authtest

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// FakeSMTP 는 STARTTLS 를 광고하지 않는 최소 SMTP 서버다. 받은 메시지를 모은다(시험 전용: TLS 없는 연결은 운영 코드가 거부하므로
// 로컬 시험에서만 PL_SMTP_INSECURE=1 로 붙는다).
type FakeSMTP struct {
	Host, Port string
	mu         sync.Mutex
	msgs       []string
}

// NewFakeSMTP 는 127.0.0.1 의 빈 포트에서 서버를 띄운다.
func NewFakeSMTP(t testing.TB) *FakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &FakeSMTP{}
	f.Host, f.Port, _ = net.SplitHostPort(ln.Addr().String())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *FakeSMTP) serve(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	var data strings.Builder
	inData := false
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.msgs = append(f.msgs, data.String())
				f.mu.Unlock()
				data.Reset()
				w("250 ok")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		switch cmd := strings.ToUpper(line); {
		case strings.HasPrefix(cmd, "EHLO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "DATA"):
			inData = true
			w("354 go")
		case strings.HasPrefix(cmd, "QUIT"):
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

// Messages 는 지금까지 받은 메시지(헤더·본문 원문)다.
func (f *FakeSMTP) Messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}
