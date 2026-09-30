// Package alert sends the report over SMTP using only the standard library.
// The password is never stored in the registry: it is read from the env var
// named there, so the JSON stays safe to commit.
package alert

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
)

// Sender delivers mail.
type Sender struct {
	cfg registry.EmailCfg
	// SendFunc is swappable so tests do not need a network.
	SendFunc func(cfg registry.EmailCfg, pass, subject, body string) error
}

// New builds a sender for the given config.
func New(cfg registry.EmailCfg) *Sender {
	return &Sender{cfg: cfg, SendFunc: smtpSend}
}

// ErrNoMailer signals that email delivery is not configured at all, which is
// the normal case on a fresh install. It is not an error worth alerting on.
var ErrNoMailer = fmt.Errorf("email not configured")

// Send delivers subject and body. Returns ErrNoMailer when Email.Host is empty.
func (s *Sender) Send(subject, body string) error {
	if s.cfg.Host == "" || s.cfg.To == "" {
		return ErrNoMailer
	}
	pass := ""
	if s.cfg.PassEnvVar != "" {
		pass = os.Getenv(s.cfg.PassEnvVar)
		if pass == "" {
			return fmt.Errorf("env %s is empty: cannot authenticate to %s", s.cfg.PassEnvVar, s.cfg.Host)
		}
	}
	return s.SendFunc(s.cfg, pass, subject, body)
}

// Configured reports whether email delivery is possible.
func (s *Sender) Configured() bool {
	return s.cfg.Host != "" && s.cfg.To != ""
}

func smtpSend(cfg registry.EmailCfg, pass, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	msg := buildMessage(cfg, subject, body)

	// Implicit TLS (port 465) is what Gmail wants for the app-password flow
	// documented in knowledge/selectel-vps.md; STARTTLS on 587 also works.
	var client *smtp.Client
	if cfg.UseTLS {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("tls dial %s: %w", addr, err)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp client %s: %w", cfg.Host, err)
		}
	} else {
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		client = c
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok && !cfg.UseTLS {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.User != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fmt.Errorf("%s offers no AUTH mechanism", cfg.Host)
		}
		if err := client.Auth(smtp.PlainAuth("", cfg.User, pass, cfg.Host)); err != nil {
			return fmt.Errorf("auth as %s: %w", cfg.User, err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM %s: %w", cfg.From, err)
	}
	if err := client.Rcpt(cfg.To); err != nil {
		return fmt.Errorf("RCPT TO %s: %w", cfg.To, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		w.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

func buildMessage(cfg registry.EmailCfg, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", cfg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	// Dot-stuffing: a line consisting of a single dot would end the DATA
	// command early. Probe details can contain anything.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(line + "\r\n")
	}
	return b.String()
}