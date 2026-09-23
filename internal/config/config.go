// Package config loads and validates the google2ipa configuration file.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Google      Google      `yaml:"google"`
	FreeIPA     FreeIPA     `yaml:"freeipa"`
	Sync        Sync        `yaml:"sync"`
	Offboarding Offboarding `yaml:"offboarding"`
	Notify      Notify      `yaml:"notify"`
	Log         Log         `yaml:"log"`
}

type Google struct {
	CredentialsFile     string   `yaml:"credentials_file"`
	ServiceAccountEmail string   `yaml:"service_account_email"`
	AdminEmail          string   `yaml:"admin_email"`
	Customer            string   `yaml:"customer"`
	Query               string   `yaml:"query"`
	OrgUnits            []string `yaml:"org_units"`
	Domains             []string `yaml:"domains"`
}

type FreeIPA struct {
	URL                string `yaml:"url"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// Host returns host[:port] of the FreeIPA URL, as expected by go-freeipa.
func (f FreeIPA) Host() string {
	u, err := url.Parse(f.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

type Sync struct {
	ManagedGroup      string              `yaml:"managed_group"`
	DefaultGroups     []string            `yaml:"default_groups"`
	GroupMapping      map[string][]string `yaml:"group_mapping"`
	ExcludeUsers      []string            `yaml:"exclude_users"`
	Username          string              `yaml:"username"`
	AdoptExisting     bool                `yaml:"adopt_existing"`
	MaxDisablePercent int                 `yaml:"max_disable_percent"`
	Concurrency       int                 `yaml:"concurrency"`
}

// GoogleGroups returns the Google group emails used in group_mapping, sorted.
func (s Sync) GoogleGroups() []string {
	keys := make([]string, 0, len(s.GroupMapping))
	for k := range s.GroupMapping {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

type Offboarding struct {
	Disable     bool     `yaml:"disable"`
	DeleteAfter Duration `yaml:"delete_after"`
	Preserve    bool     `yaml:"preserve"`
}

type Notify struct {
	SMTP    SMTP    `yaml:"smtp"`
	Welcome Welcome `yaml:"welcome"`
	Admin   Admin   `yaml:"admin"`
}

type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type Welcome struct {
	Enabled      bool   `yaml:"enabled"`
	Subject      string `yaml:"subject"`
	TemplateFile string `yaml:"template_file"`
}

type Admin struct {
	Enabled bool     `yaml:"enabled"`
	To      []string `yaml:"to"`
}

type Log struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Duration accepts Go durations plus a whole-day suffix, e.g. "30d".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration (e.g. delete_after) at line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

func Default() *Config {
	return &Config{
		Google:      Google{Customer: "my_customer"},
		Sync:        Sync{ManagedGroup: "google2ipa-managed", Username: "local_part", MaxDisablePercent: 20, Concurrency: 4},
		Offboarding: Offboarding{Disable: true, DeleteAfter: Duration(30 * 24 * time.Hour), Preserve: true},
		Notify: Notify{
			SMTP:    SMTP{Port: 587},
			Welcome: Welcome{Subject: "Your account is ready"},
		},
		Log: Log{Format: "json", Level: "info"},
	}
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads path, expands ${VAR} references, applies defaults and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var missing []string
	expanded := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("unset environment variables referenced in config: %s", strings.Join(missing, ", "))
	}

	cfg := Default()
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

func (c *Config) normalize() {
	c.Google.Domains = lower(c.Google.Domains)
	c.Sync.ExcludeUsers = lower(c.Sync.ExcludeUsers)
	m := make(map[string][]string, len(c.Sync.GroupMapping))
	for k, v := range c.Sync.GroupMapping {
		m[strings.ToLower(strings.TrimSpace(k))] = v
	}
	c.Sync.GroupMapping = m
}

func readable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func (c *Config) validate() error {
	var errs []error
	req := func(v, name string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	req(c.Google.AdminEmail, "google.admin_email")
	if c.Google.CredentialsFile == "" && c.Google.ServiceAccountEmail == "" {
		errs = append(errs, errors.New("google.credentials_file or google.service_account_email is required"))
	}
	req(c.FreeIPA.URL, "freeipa.url")
	if u, err := url.Parse(c.FreeIPA.URL); c.FreeIPA.URL != "" && (err != nil || u.Scheme != "https" || u.Host == "") {
		errs = append(errs, errors.New("freeipa.url must be an https:// URL"))
	}
	req(c.FreeIPA.Username, "freeipa.username")
	req(c.FreeIPA.Password, "freeipa.password")
	if c.FreeIPA.CAFile != "" {
		if err := readable(c.FreeIPA.CAFile); err != nil {
			errs = append(errs, fmt.Errorf("freeipa.ca_file: %w", err))
		}
	}
	req(c.Sync.ManagedGroup, "sync.managed_group")
	if c.Sync.Username != "local_part" && c.Sync.Username != "email" {
		errs = append(errs, errors.New("sync.username must be local_part or email"))
	}
	if c.Sync.MaxDisablePercent < 0 || c.Sync.MaxDisablePercent > 100 {
		errs = append(errs, errors.New("sync.max_disable_percent must be between 0 and 100"))
	}
	if c.Sync.Concurrency < 1 {
		errs = append(errs, errors.New("sync.concurrency must be >= 1"))
	}
	targets := slices.Clone(c.Sync.DefaultGroups)
	for _, v := range c.Sync.GroupMapping {
		targets = append(targets, v...)
	}
	if slices.Contains(targets, c.Sync.ManagedGroup) {
		errs = append(errs, errors.New("sync.managed_group must not appear in default_groups or group_mapping"))
	}
	n := c.Notify
	if n.Welcome.Enabled || n.Admin.Enabled {
		req(n.SMTP.Host, "notify.smtp.host")
		req(n.SMTP.From, "notify.smtp.from")
	}
	if n.Admin.Enabled && len(n.Admin.To) == 0 {
		errs = append(errs, errors.New("notify.admin.to is required when admin notifications are enabled"))
	}
	if n.Welcome.TemplateFile != "" {
		if err := readable(n.Welcome.TemplateFile); err != nil {
			errs = append(errs, fmt.Errorf("notify.welcome.template_file: %w", err))
		}
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		errs = append(errs, errors.New("log.format must be json or text"))
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Log.Level)); err != nil {
		errs = append(errs, fmt.Errorf("log.level: %w", err))
	}
	return errors.Join(errs...)
}
