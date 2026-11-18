package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer 는 메일 발송 수단이다. 구현은 SMTP(표준 net/smtp)이고 시험은 가짜를 넣는다.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// SMTPConfig 는 메일 서버 설정이다. 비밀번호는 환경변수(PL_SMTP_PASSWORD)로만 받는다.
type SMTPConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
	// Insecure 는 TLS 없는 연결을 허용한다(로컬 시험 전용: PL_SMTP_INSECURE=1 이고 PL_BASE_URL 이 로컬일 때만 호출하는 쪽이 켠다).
	Insecure bool
}

type smtpMailer struct{ cfg SMTPConfig }

// NewSMTPMailer 는 SMTP 발송기를 만든다. 포트 465 는 암묵 TLS, 그 밖에는 STARTTLS 를 요구하고 TLS 가 없으면 거부한다.
func NewSMTPMailer(c SMTPConfig) (Mailer, error) {
	if c.Host == "" || c.From == "" {
		return nil, errors.New("auth: PL_SMTP_HOST 와 PL_SMTP_FROM 이 필요합니다")
	}
	if c.Port == "" {
		c.Port = "587"
	}
	if strings.ContainsAny(c.From, "\r\n") {
		return nil, errors.New("auth: PL_SMTP_FROM 에 줄바꿈이 있습니다")
	}
	return &smtpMailer{cfg: c}, nil
}

func (m *smtpMailer) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to+subject, "\r\n") {
		return errors.New("메일 헤더에 줄바꿈을 쓸 수 없습니다") // 헤더 주입 방지
	}
	addr := net.JoinHostPort(m.cfg.Host, m.cfg.Port)
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if m.cfg.Port == "465" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if m.cfg.Port != "465" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		} else if !m.cfg.Insecure {
			return errors.New("메일 서버가 STARTTLS 를 지원하지 않아 보내지 않습니다(TLS 없는 연결 거부)")
		}
	}
	if m.cfg.User != "" {
		if _, isTLS := c.TLSConnectionState(); !isTLS && !m.cfg.Insecure {
			return errors.New("TLS 없이 메일 서버 인증을 보내지 않습니다")
		}
		if err := c.Auth(plainAuth{m.cfg.User, m.cfg.Password}); err != nil {
			return err
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		m.cfg.From, to, base64.StdEncoding.EncodeToString([]byte(subject)), strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// plainAuth 는 net/smtp 의 PlainAuth 와 달리 로컬 시험 서버(암호화 없음)에서도 쓸 수 있다. 호출 전에 TLS 여부를 이미 검사한다.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (plainAuth) Next([]byte, bool) ([]byte, error) { return nil, nil }
