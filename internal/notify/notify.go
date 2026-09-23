// Package notify sends welcome mails and admin summaries over SMTP.
package notify

import (
	"bytes"
	"crypto/tls"
	"embed"
	"fmt"
	"mime"
	"net"
	"net/mail"
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
	m := &Mailer{cfg: cfg, url: ipaURL, send: sendMail}
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
	from := m.cfg.SMTP.From
	if a, err := mail.ParseAddress(from); err == nil {
		from = a.Address // "IT <it@x>" is valid in the header, not in MAIL FROM
	}
	if err := m.send(addr, auth, from, to, msg.Bytes()); err != nil {
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

// timeout bounds a whole SMTP conversation; a variable so tests can shorten it.
var timeout = 30 * time.Second

// sendMail is smtp.SendMail with a deadline, plus implicit TLS on port 465.
func sendMail(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, port, _ := net.SplitHostPort(addr)
	d := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if port == "465" {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && port != "465" {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
