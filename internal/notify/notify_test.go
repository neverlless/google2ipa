package notify

import (
	"errors"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if err := m.Welcome(u, "R4nd0m"); err != nil {
		t.Fatal(err)
	}
	if len(*out) != 1 {
		t.Fatalf("sent %d", len(*out))
	}
	s := (*out)[0]
	for _, want := range []string{"From: it@example.com\r\n", "To: john@example.com\r\n", "Subject: =?utf-8?q?", "Date: ", "Content-Type: text/plain; charset=utf-8", "Hello John,\r\n", "Temporary password: R4nd0m", "https://ipa.example.com"} {
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
