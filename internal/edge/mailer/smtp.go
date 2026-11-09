package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// StartTLS policies for Config.StartTLS.
const (
	StartTLSNone          = "none"          // never negotiate TLS (default; plain office MTA)
	StartTLSOpportunistic = "opportunistic" // use STARTTLS when the server offers it
	StartTLSAlways        = "always"        // fail when the server does not offer STARTTLS
)

// Config holds the SMTP settings. The zero values of the optional fields work
// for a plain, unauthenticated SMTP relay without TLS.
type Config struct {
	Host           string
	Port           int // default 25
	FromEmail      string
	FromName       string
	ClientHostname string // EHLO name, default "localhost"
	TimeoutSeconds int    // default 10
	StartTLS       string // "none" | "opportunistic" | "always"; default "none"
	User, Password string // optional AUTH PLAIN/LOGIN
}

// Enabled reports whether enough is configured to send email.
func (c Config) Enabled() bool { return c.Host != "" && c.FromEmail != "" }

func (c Config) port() int {
	if c.Port <= 0 {
		return 25
	}
	return c.Port
}

func (c Config) timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

func (c Config) clientHostname() string {
	if c.ClientHostname == "" {
		return "localhost"
	}
	return c.ClientHostname
}

// InlineImage is an image referenced from the HTML through cid:<CID>.
type InlineImage struct {
	CID         string
	ContentType string
	Data        []byte
}

// Message is one email to one recipient.
type Message struct {
	ToEmail, ToName string
	Subject         string
	HTML, Text      string
	Inline          []InlineImage // empty = plain multipart/alternative
}

// Sender sends email (replaced by a fake in unit tests).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTPMailer sends through SMTP, opening a new connection per message.
type SMTPMailer struct {
	Cfg Config
}

var _ Sender = (*SMTPMailer)(nil)

func (m *SMTPMailer) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: m.Cfg.Host, MinVersion: tls.VersionTLS12}
}

// Ping checks TCP reachability of the SMTP server.
func (m *SMTPMailer) Ping(ctx context.Context) error {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.Cfg.Host, strconv.Itoa(m.Cfg.port())))
	if err != nil {
		return err
	}
	return conn.Close()
}

// Send delivers msg. Headers are validated before any connection is made.
func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	c := m.Cfg
	raw, err := m.build(msg)
	if err != nil {
		return err
	}
	timeout := c.timeout()
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.port()))
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp connect %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer func() { _ = cl.Close() }()
	if err := cl.Hello(c.clientHostname()); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	switch c.StartTLS {
	case StartTLSOpportunistic, StartTLSAlways:
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(m.tlsConfig()); err != nil {
				return fmt.Errorf("smtp STARTTLS: %w", err)
			}
		} else if c.StartTLS == StartTLSAlways {
			return errors.New("smtp: StartTLS=always but server does not offer STARTTLS")
		}
	}
	if c.User != "" {
		ok, mechs := cl.Extension("AUTH")
		if !ok {
			return errors.New("smtp: user configured but server does not offer AUTH")
		}
		var a smtp.Auth = &plainAuth{user: c.User, pass: c.Password}
		up := strings.ToUpper(mechs)
		if !strings.Contains(" "+up+" ", " PLAIN ") && strings.Contains(up, "LOGIN") {
			a = &loginAuth{user: c.User, pass: c.Password}
		}
		if err := cl.Auth(a); err != nil {
			return fmt.Errorf("smtp AUTH: %w", err)
		}
	}
	if err := cl.Mail(c.FromEmail); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := cl.Rcpt(msg.ToEmail); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of DATA: %w", err)
	}
	return cl.Quit()
}

// build assembles the MIME message: multipart/related { multipart/alternative
// { text, html }, inline images } or plain multipart/alternative without images.
func (m *SMTPMailer) build(msg Message) ([]byte, error) {
	c := m.Cfg
	for _, v := range []string{msg.ToEmail, msg.ToName, msg.Subject, c.FromEmail, c.FromName} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("email header contains a line break")
		}
	}
	if c.FromEmail == "" || msg.ToEmail == "" {
		return nil, errors.New("email sender and recipient are required")
	}
	from := (&mail.Address{Name: c.FromName, Address: c.FromEmail}).String()
	to := (&mail.Address{Name: msg.ToName, Address: msg.ToEmail}).String()
	boundary := "drawspace-" + randHex(12)
	domain := c.FromEmail[strings.LastIndex(c.FromEmail, "@")+1:]
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+randHex(16)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	h("X-Auto-Response-Suppress", "All")
	altBoundary := boundary
	if len(msg.Inline) > 0 {
		altBoundary = "drawspace-alt-" + randHex(12)
		h("Content-Type", `multipart/related; type="multipart/alternative"; boundary="`+boundary+`"`)
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString(`Content-Type: multipart/alternative; boundary="` + altBoundary + `"` + "\r\n\r\n")
	} else {
		h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		b.WriteString("\r\n")
	}
	for _, part := range []struct{ ct, body string }{{"text/plain", msg.Text}, {"text/html", msg.HTML}} {
		b.WriteString("--" + altBoundary + "\r\n")
		b.WriteString("Content-Type: " + part.ct + "; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + altBoundary + "--\r\n")
	for _, im := range msg.Inline {
		if strings.ContainsAny(im.CID+im.ContentType, "\r\n\"<> ") {
			return nil, errors.New("invalid inline image header")
		}
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + im.ContentType + `; name="` + im.CID + `.png"` + "\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\nContent-ID: <" + im.CID + ">\r\n")
		b.WriteString(`Content-Disposition: inline; filename="` + im.CID + `.png"` + "\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(im.Data)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	if len(msg.Inline) > 0 {
		b.WriteString("--" + boundary + "--\r\n")
	}
	return b.Bytes(), nil
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// plainAuth is AUTH PLAIN without net/smtp's TLS requirement; the TLS decision
// lives in Config.StartTLS.
type plainAuth struct{ user, pass string }

func (a *plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a *plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected AUTH PLAIN challenge")
	}
	return nil, nil
}

// loginAuth is AUTH LOGIN for servers that do not offer PLAIN.
type loginAuth struct{ user, pass string }

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a *loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(from))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unknown AUTH LOGIN challenge")
}
