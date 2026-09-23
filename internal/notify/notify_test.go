package notify

import (
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

type sent struct {
	addr string
	from string
	to   []string
	msg  string
}

func mailer(t *testing.T, cfg config.Notify) (*Mailer, *[]sent) {
	t.Helper()
	m, err := New(cfg, "https://ipa.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var out []sent
	m.send = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		out = append(out, sent{addr, from, to, string(msg)})
		return nil
	}
	return m, &out
}

var base = config.Notify{SMTP: config.SMTP{Host: "smtp.example.com", Port: 587, From: "it@example.com"}}

func TestWelcome(t *testing.T) {
	cfg := base
	cfg.Welcome = config.Welcome{Enabled: true, Subject: "Ваш аккаунт"}
	m, out := mailer(t, cfg)
	u := reconcile.User{UID: "john", GoogleUser: reconcile.GoogleUser{Email: "john@example.com", GivenName: "John"}}
	if err := m.Welcome(u, "fake-generated-value"); err != nil {
		t.Fatal(err)
	}
	if len(*out) != 1 {
		t.Fatalf("sent %d", len(*out))
	}
	s := (*out)[0]
	for _, want := range []string{"From: it@example.com\r\n", "To: john@example.com\r\n", "Subject: =?utf-8?q?", "Date: ", "Content-Type: text/plain; charset=utf-8", "Hello John,\r\n", "Temporary password: fake-generated-value", "https://ipa.example.com"} {
		if !strings.Contains(s.msg, want) {
			t.Errorf("message missing %q:\n%s", want, s.msg)
		}
	}
	if s.addr != "smtp.example.com:587" || s.to[0] != "john@example.com" {
		t.Errorf("envelope = %+v", s)
	}
}

func TestDisabledKindsSendNothing(t *testing.T) {
	m, out := mailer(t, base)
	_ = m.Welcome(reconcile.User{UID: "a"}, "x")
	_ = m.Summary(reconcile.Report{Errors: []string{"boom"}})
	if len(*out) != 0 {
		t.Errorf("sent %d", len(*out))
	}
}

func TestSummary(t *testing.T) {
	cfg := base
	cfg.Admin = config.Admin{Enabled: true, To: []string{"ops@example.com"}}
	m, out := mailer(t, cfg)
	if err := m.Summary(reconcile.Report{}); err != nil || len(*out) != 0 {
		t.Fatalf("empty report must not mail: %v %d", err, len(*out))
	}
	r := reconcile.Report{Plan: reconcile.Plan{Disable: []string{"gone"}, Braked: false}, Errors: []string{"create x: boom"}}
	if err := m.Summary(r); err != nil {
		t.Fatal(err)
	}
	msg := (*out)[0].msg
	for _, want := range []string{"Subject: google2ipa: 1 change(s), 1 error(s)", "- gone", "! create x: boom"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q:\n%s", want, msg)
		}
	}
}

func TestCustomTemplateAndHeaderInjection(t *testing.T) {
	tpl := filepath.Join(t.TempDir(), "w.tmpl")
	_ = os.WriteFile(tpl, []byte("Hi {{.UID}}"), 0o600)
	cfg := base
	cfg.Welcome = config.Welcome{Enabled: true, Subject: "x\r\nBcc: evil@example.com", TemplateFile: tpl}
	m, out := mailer(t, cfg)
	if err := m.Welcome(reconcile.User{UID: "a", GoogleUser: reconcile.GoogleUser{Email: "a@example.com"}}, "p"); err != nil {
		t.Fatal(err)
	}
	msg := (*out)[0].msg
	if strings.Contains(msg, "\r\nBcc:") {
		t.Errorf("header injection:\n%s", msg)
	}
	if !strings.HasSuffix(msg, "Hi a\r\n") {
		t.Errorf("custom template not used:\n%q", msg)
	}
}

func TestSendError(t *testing.T) {
	cfg := base
	cfg.Welcome = config.Welcome{Enabled: true, Subject: "s"}
	m, _ := mailer(t, cfg)
	m.send = func(string, smtp.Auth, string, []string, []byte) error { return errors.New("down") }
	if err := m.Welcome(reconcile.User{UID: "a", GoogleUser: reconcile.GoogleUser{Email: "a@example.com"}}, "p"); err == nil {
		t.Error("want error")
	}
}

func TestStalledSMTPTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() { // accept and never speak
		c, err := ln.Accept()
		if err == nil {
			defer func() { _ = c.Close() }()
			time.Sleep(5 * time.Second)
		}
	}()
	old := timeout
	timeout = 300 * time.Millisecond
	defer func() { timeout = old }()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	cfg := config.Notify{SMTP: config.SMTP{Host: "127.0.0.1", Port: p, From: "it@example.com"},
		Welcome: config.Welcome{Enabled: true, Subject: "s"}}
	m, err := New(cfg, "https://ipa.example.com")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Welcome(reconcile.User{UID: "a", GoogleUser: reconcile.GoogleUser{Email: "a@example.com"}}, "p"); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestEnvelopeFromDisplayName(t *testing.T) {
	cfg := base
	cfg.SMTP.From = "IT Team <it@example.com>"
	cfg.Welcome = config.Welcome{Enabled: true, Subject: "s"}
	m, out := mailer(t, cfg)
	if err := m.Welcome(reconcile.User{UID: "a", GoogleUser: reconcile.GoogleUser{Email: "a@example.com"}}, "p"); err != nil {
		t.Fatal(err)
	}
	s := (*out)[0]
	if s.from != "it@example.com" || !strings.Contains(s.msg, "From: IT Team <it@example.com>\r\n") {
		t.Errorf("envelope %q, msg:\n%s", s.from, s.msg)
	}
}

// fakeSMTP speaks just enough SMTP for one message and returns what it received.
func fakeSMTP(t *testing.T) (port int, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		tp := textproto.NewConn(c)
		var rec strings.Builder
		_ = tp.PrintfLine("220 fake")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			rec.WriteString(line + "\n")
			switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
			case "EHLO", "HELO":
				_ = tp.PrintfLine("250 fake")
			case "DATA":
				_ = tp.PrintfLine("354 go")
				body, _ := tp.ReadDotLines()
				rec.WriteString(strings.Join(body, "\n") + "\n")
				_ = tp.PrintfLine("250 ok")
			case "QUIT":
				_ = tp.PrintfLine("221 bye")
				ch <- rec.String()
				return
			default:
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(ps)
	return port, ch
}

func TestSendMailRealSMTP(t *testing.T) {
	port, got := fakeSMTP(t)
	cfg := config.Notify{SMTP: config.SMTP{Host: "127.0.0.1", Port: port, From: "IT <it@example.com>"},
		Welcome: config.Welcome{Enabled: true, Subject: "hi"}}
	m, err := New(cfg, "https://ipa.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Welcome(reconcile.User{UID: "a", GoogleUser: reconcile.GoogleUser{Email: "a@example.com"}}, "fake-otp"); err != nil {
		t.Fatal(err)
	}
	rec := <-got
	for _, want := range []string{"MAIL FROM:<it@example.com>", "RCPT TO:<a@example.com>", "Temporary password: fake-otp"} {
		if !strings.Contains(rec, want) {
			t.Errorf("missing %q in:\n%s", want, rec)
		}
	}
}

func TestSummaryNotRepeatedForSameErrors(t *testing.T) {
	cfg := base
	cfg.Admin = config.Admin{Enabled: true, To: []string{"ops@example.com"}}
	m, out := mailer(t, cfg)
	stuck := reconcile.Report{Errors: []string{"skip uid \"jane\": collision"}}
	for range 3 {
		_ = m.Summary(stuck)
	}
	if len(*out) != 1 {
		t.Fatalf("same errors mailed %d times, want 1", len(*out))
	}
	_ = m.Summary(reconcile.Report{Errors: []string{"other"}})                                               // new error: mail
	_ = m.Summary(reconcile.Report{Plan: reconcile.Plan{Disable: []string{"x"}}, Errors: []string{"other"}}) // changes: mail
	_ = m.Summary(reconcile.Report{})                                                                        // all good: nothing
	_ = m.Summary(stuck)                                                                                     // came back: mail
	if len(*out) != 4 {
		t.Errorf("sent %d, want 4", len(*out))
	}
}
