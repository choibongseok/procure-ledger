package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/auth/authtest"
)

// AC-1023: TLS(STARTTLS)를 지원하지 않는 SMTP 서버로는 보내지 않는다. 로컬 시험 예외(Insecure)에서만 전달된다.
func TestSMTPRefusesWithoutTLS(t *testing.T) {
	f := authtest.NewFakeSMTP(t)
	port, got := f.Port, f.Messages
	m, err := NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: port, From: "erp@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), "a@example.test", "제목", "본문"); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("TLS 없는 연결이 허용됨: %v", err)
	}
	if len(got()) != 0 {
		t.Fatal("거부했는데 메시지가 전달됨")
	}
	m2, _ := NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: port, From: "erp@example.test", Insecure: true})
	if err := m2.Send(context.Background(), "a@example.test", "제목", "본문 링크"); err != nil {
		t.Fatal(err)
	}
	if msgs := got(); len(msgs) != 1 || !strings.Contains(msgs[0], "본문 링크") {
		t.Fatalf("전달된 메시지: %v", msgs)
	}
	// 인증 정보가 있으면 TLS 없이는 보내지 않는다.
	m3, _ := NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: port, User: "u", Password: "p", From: "erp@example.test"})
	if err := m3.Send(context.Background(), "a@example.test", "s", "b"); err == nil {
		t.Fatal("TLS 없이 인증이 시도됨")
	}
	// 헤더 주입 방지
	if err := m2.Send(context.Background(), "a@example.test\r\nBcc: x@y.z", "s", "b"); err == nil {
		t.Fatal("헤더 주입이 통과함")
	}
}
