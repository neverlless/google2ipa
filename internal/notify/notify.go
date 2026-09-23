// Package notify sends welcome mails and admin summaries over SMTP.
package notify

import (
	"bytes"
	"embed"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

//go:embed templates/*.tmpl
var builtin embed.FS

type WelcomeData struct {
	UID, Email, GivenName, FamilyName, FullName, Password, URL string
}

type Mailer struct {
	cfg     config.Notify
	url     string
	welcome *template.Template
	summary *template.Template
	send    func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func New(cfg config.Notify, ipaURL string) (*Mailer, error) {
	m := &Mailer{cfg: cfg, url: ipaURL, send: smtp.SendMail}
	var err error
	if cfg.Welcome.TemplateFile != "" {
		m.welcome, err = template.ParseFiles(cfg.Welcome.TemplateFile)
	} else {
		m.welcome, err = template.ParseFS(builtin, "templates/welcome.tmpl")
	}
	if err != nil {
		return nil, fmt.Errorf("welcome template: %w", err)
	}
	m.summary = template.Must(template.ParseFS(builtin, "templates/summary.tmpl"))
	return m, nil
}

var headerSafe = strings.NewReplacer("\r", "", "\n", "")

func (m *Mailer) mail(to []string, subject string, tpl *template.Template, data any) error {
	var body bytes.Buffer
	if err := tpl.Execute(&body, data); err != nil {
		return fmt.Errorf("render %s: %w", tpl.Name(), err)
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		headerSafe.Replace(m.cfg.SMTP.From),
		headerSafe.Replace(strings.Join(to, ", ")),
		mime.QEncoding.Encode("utf-8", headerSafe.Replace(subject)),
		time.Now().Format(time.RFC1123Z))
	text := strings.ReplaceAll(body.String(), "\r\n", "\n")
	msg.WriteString(strings.ReplaceAll(strings.TrimRight(text, "\n")+"\n", "\n", "\r\n"))

	var auth smtp.Auth
	if m.cfg.SMTP.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTP.Username, m.cfg.SMTP.Password, m.cfg.SMTP.Host)
	}
	addr := net.JoinHostPort(m.cfg.SMTP.Host, strconv.Itoa(m.cfg.SMTP.Port))
	if err := m.send(addr, auth, m.cfg.SMTP.From, to, msg.Bytes()); err != nil {
		return fmt.Errorf("smtp %s: %w", addr, err)
	}
	return nil
}

func (m *Mailer) Welcome(u reconcile.User, password string) error {
	if !m.cfg.Welcome.Enabled {
		return nil
	}
	return m.mail([]string{u.Email}, m.cfg.Welcome.Subject, m.welcome, WelcomeData{
		UID: u.UID, Email: u.Email, GivenName: u.GivenName, FamilyName: u.FamilyName,
		FullName: u.FullName, Password: password, URL: m.url,
	})
}

func (m *Mailer) Summary(r reconcile.Report) error {
	if !m.cfg.Admin.Enabled || (r.Plan.Empty() && r.OK()) {
		return nil
	}
	p := r.Plan
	changes := len(p.Create) + len(p.Adopt) + len(p.Enable) + len(p.Disable) + len(p.Delete) + len(p.AddGroups) + len(p.RemoveGroups)
	subject := fmt.Sprintf("google2ipa: %d change(s), %d error(s)", changes, len(r.Errors))
	return m.mail(m.cfg.Admin.To, subject, m.summary, r)
}
