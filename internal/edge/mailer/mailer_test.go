package mailer

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func baseInvite() Invite {
	exp := time.Date(2026, 11, 16, 16, 59, 0, 0, time.UTC) // 23.59 WIB
	return Invite{
		ToName: "Grace", InviterName: "Galih", InviterUsername: "galih22598",
		RecipientUsername: "grace23167", DocTitle: "test", Role: "editor",
		ExpiresAt: &exp, Link: "https://drawspace.lab.local/invite/abc", SiteHost: "drawspace.lab.local",
	}
}

func TestRenderEscapesUserData(t *testing.T) {
	in := baseInvite()
	in.DocTitle = `<script>alert(1)</script>"x'`
	in.ToName = `<img src=x onerror=alert(1)>`
	in.InviterName = `"><b>bold</b>`
	r, err := RenderInvite(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>alert", "<img src=x", "<b>bold</b>"} {
		if strings.Contains(r.HTML, bad) {
			t.Errorf("unescaped %q in HTML", bad)
		}
	}
	if !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Error("expected escaped script tag")
	}
}

func TestSubject(t *testing.T) {
	cases := []struct{ title, want string }{
		{"test", "Undangan akses diagram 'test'"},
		{"  a\r\nb\tc\x00d  ", "Undangan akses diagram 'a  b c d'"},
		{strings.Repeat("é", 80), "Undangan akses diagram '" + strings.Repeat("é", 80) + "'"},
		{strings.Repeat("é", 81), "Undangan akses diagram '" + strings.Repeat("é", 80) + "…'"},
	}
	for _, c := range cases {
		in := baseInvite()
		in.DocTitle = c.title
		r, err := RenderInvite(in)
		if err != nil {
			t.Fatal(err)
		}
		if r.Subject != c.want {
			t.Errorf("subject = %q, want %q", r.Subject, c.want)
		}
		if strings.ContainsAny(r.Subject, "\r\n") {
			t.Error("subject contains newline")
		}
	}
}

func TestRoleLabels(t *testing.T) {
	for role, want := range map[string][2]string{
		"viewer": {"Viewer", "Dapat melihat diagram."},
		"editor": {"Editor", "Dapat melihat dan mengubah diagram."},
	} {
		in := baseInvite()
		in.Role = role
		r, _ := RenderInvite(in)
		if !strings.Contains(r.HTML, ">"+want[0]+"</span>") || !strings.Contains(r.HTML, want[1]) {
			t.Errorf("role %s: labels missing", role)
		}
		if !strings.Contains(r.Text, want[1]) {
			t.Errorf("role %s: text missing description", role)
		}
	}
}

func TestExpiry(t *testing.T) {
	in := baseInvite()
	r, _ := RenderInvite(in)
	if !strings.Contains(r.HTML, "16 Nov 2026, 23.59 WIB") {
		t.Error("WIB expiry missing")
	}
	agu := time.Date(2026, 8, 3, 1, 5, 0, 0, time.UTC)
	if got := formatExpiry(&agu); got != "3 Agu 2026, 08.05 WIB" {
		t.Errorf("got %q", got)
	}
	mei := time.Date(2026, 5, 31, 20, 0, 0, 0, time.UTC) // crosses into 1 Jun WIB
	if got := formatExpiry(&mei); got != "1 Jun 2026, 03.00 WIB" {
		t.Errorf("got %q", got)
	}
	in.ExpiresAt = nil
	r, _ = RenderInvite(in)
	if !strings.Contains(r.HTML, "Tidak kedaluwarsa") || !strings.Contains(r.Text, "Tidak kedaluwarsa") {
		t.Error("never text missing")
	}
}

func TestRenderInlineAndLink(t *testing.T) {
	r, _ := RenderInvite(baseInvite())
	if len(r.Inline) != 2 || len(r.Inline[0].Data) == 0 || len(r.Inline[1].Data) == 0 {
		t.Fatal("inline images missing")
	}
	for _, s := range []string{"cid:drawspace-icon", "cid:drawspace-header-decoration", `href="https://drawspace.lab.local/invite/abc"`} {
		if !strings.Contains(r.HTML, s) {
			t.Errorf("HTML missing %q", s)
		}
	}
	if !strings.Contains(r.Text, "https://drawspace.lab.local/invite/abc") {
		t.Error("text missing link")
	}
}

func TestCRLFRejected(t *testing.T) {
	m := &SMTPMailer{Cfg: Config{Host: "127.0.0.1", Port: 1, FromEmail: "noreply@btn.co.id", FromName: "DrawSpace"}}
	for _, msg := range []Message{
		{ToEmail: "a@b.c\r\nBcc: x@y.z", Subject: "s"},
		{ToEmail: "a@b.c", ToName: "x\nBcc: y", Subject: "s"},
		{ToEmail: "a@b.c", Subject: "s\r\nBcc: x@y.z"},
	} {
		if err := m.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "line break") {
			t.Errorf("expected line break error, got %v", err)
		}
	}
	m.Cfg.FromEmail = "a@b.c\r\nX: y"
	if _, err := m.build(Message{ToEmail: "a@b.c", Subject: "s"}); err == nil {
		t.Error("expected From rejection")
	}
}

func TestEnabled(t *testing.T) {
	if (Config{}).Enabled() || (Config{Host: "h"}).Enabled() || !(Config{Host: "h", FromEmail: "a@b.c"}).Enabled() {
		t.Error("Enabled mismatch")
	}
}

// fakeSMTP speaks minimal SMTP and returns the received DATA on a channel.
func fakeSMTP(t *testing.T) (port int, data chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data = make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			serveSMTP(c, data)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, data
}

// serveSMTP handles one connection (Ping opens and closes one before Send connects).
func serveSMTP(c net.Conn, data chan string) {
	{
		defer func() { _ = c.Close() }()
		rd := bufio.NewReader(c)
		say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250-fake")
				say("250 8BITMIME")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 OK")
			case cmd == "DATA":
				say("354 go")
				var sb strings.Builder
				for {
					l, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				data <- sb.String()
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("500 ?")
			}
		}
	}
}

func TestSMTPSend(t *testing.T) {
	port, data := fakeSMTP(t)
	m := &SMTPMailer{Cfg: Config{Host: "127.0.0.1", Port: port, FromEmail: "noreply@btn.co.id", FromName: "DrawSpace", TimeoutSeconds: 5}}
	if err := m.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := RenderInvite(baseInvite())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), Message{ToEmail: "grace@btn.co.id", ToName: "Grace", Subject: r.Subject, HTML: r.HTML, Text: r.Text, Inline: r.Inline}); err != nil {
		t.Fatal(err)
	}
	var raw string
	select {
	case raw = <-data:
	case <-time.After(5 * time.Second):
		t.Fatal("no data received")
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// the display name may be sent quoted ("DrawSpace" <...>): clients show the same, so compare the parsed address
	if from, err := mail.ParseAddress(msg.Header.Get("From")); err != nil || from.Name != "DrawSpace" || from.Address != "noreply@btn.co.id" {
		t.Errorf("From = %q (%v)", msg.Header.Get("From"), err)
	}
	dec := new(mime.WordDecoder)
	if subj, _ := dec.DecodeHeader(msg.Header.Get("Subject")); subj != r.Subject {
		t.Errorf("Subject = %q", subj)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		t.Fatalf("content type %q err %v", mt, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	cids := map[string]bool{}
	sawLink := false
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pt, pp, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if pt == "multipart/alternative" {
			ar := multipart.NewReader(p, pp["boundary"])
			for {
				ap, err := ar.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(quotedprintable.NewReader(ap))
				if strings.Contains(string(b), "https://drawspace.lab.local/invite/abc") {
					sawLink = true
				}
			}
			continue
		}
		cids[strings.Trim(p.Header.Get("Content-ID"), "<>")] = true
	}
	if !cids["drawspace-icon"] || !cids["drawspace-header-decoration"] {
		t.Errorf("cid parts missing: %v", cids)
	}
	if !sawLink {
		t.Error("body does not contain link")
	}
}
