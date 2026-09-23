package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimal = `
google:
  credentials_file: /sa.json
  admin_email: admin@example.com
freeipa:
  url: https://ipa.example.com
  username: svc
  password: ${G2I_TEST_PASS}
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndEnv(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "s3cret")
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FreeIPA.Password != "s3cret" {
		t.Errorf("password = %q", cfg.FreeIPA.Password)
	}
	if cfg.FreeIPA.Host() != "ipa.example.com" {
		t.Errorf("host = %q", cfg.FreeIPA.Host())
	}
	if cfg.Google.Customer != "my_customer" || cfg.Sync.ManagedGroup != "google2ipa-managed" ||
		cfg.Sync.Username != "local_part" || cfg.Sync.MaxDisablePercent != 20 || cfg.Sync.Concurrency != 4 ||
		!cfg.Offboarding.Disable || !cfg.Offboarding.Preserve ||
		time.Duration(cfg.Offboarding.DeleteAfter) != 30*24*time.Hour ||
		cfg.Notify.SMTP.Port != 587 || cfg.Log.Format != "json" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestLoadUnsetVarIsError(t *testing.T) {
	_, err := Load(write(t, strings.ReplaceAll(minimal, "G2I_TEST_PASS", "G2I_TEST_UNSET_VAR")))
	if err == nil || !strings.Contains(err.Error(), "G2I_TEST_UNSET_VAR") {
		t.Fatalf("want error naming the variable, got %v", err)
	}
}

func TestEnvRefInCommentIgnored(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "x")
	cfg, err := Load(write(t, "# use ${ANY_VAR} for secrets\n"+minimal+"  # ${ALSO_NOT_SET}\n"))
	if err != nil {
		t.Fatalf("comments must not be expanded: %v", err)
	}
	if cfg.FreeIPA.Password != "x" {
		t.Errorf("password = %q", cfg.FreeIPA.Password)
	}
}

func TestLiteralDollarUntouched(t *testing.T) {
	cfg, err := Load(write(t, strings.ReplaceAll(minimal, "${G2I_TEST_PASS}", "pa$$word")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FreeIPA.Password != "pa$$word" {
		t.Errorf("password = %q", cfg.FreeIPA.Password)
	}
}

func TestNormalization(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "x")
	body := minimal + `
  insecure_skip_verify: false
sync:
  group_mapping:
    DevOps@Example.com: [admins]
  exclude_users: [Admin]
`
	body = strings.Replace(body, "admin_email: admin@example.com", "admin_email: admin@example.com\n  domains: [Example.COM]", 1)
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Sync.GroupMapping["devops@example.com"]; !ok {
		t.Errorf("mapping keys not lowercased: %v", cfg.Sync.GroupMapping)
	}
	if cfg.Sync.ExcludeUsers[0] != "admin" || cfg.Google.Domains[0] != "example.com" {
		t.Errorf("not lowercased: %v %v", cfg.Sync.ExcludeUsers, cfg.Google.Domains)
	}
	if got := cfg.Sync.GoogleGroups(); len(got) != 1 || got[0] != "devops@example.com" {
		t.Errorf("GoogleGroups = %v", got)
	}
}

func TestValidation(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "x")
	cases := map[string]struct{ from, to, want string }{
		"unknown key":       {"username: svc", "username: svc\n  usrname: typo", "usrname"},
		"http url":          {"https://ipa", "http://ipa", "https"},
		"no admin email":    {"admin_email: admin@example.com", "admin_email: \"\"", "admin_email"},
		"no credentials":    {"credentials_file: /sa.json", "credentials_file: \"\"", "service_account_email"},
		"bad username":      {"password: ${G2I_TEST_PASS}", "password: ${G2I_TEST_PASS}\nsync:\n  username: upn", "username"},
		"bad percent":       {"password: ${G2I_TEST_PASS}", "password: ${G2I_TEST_PASS}\nsync:\n  max_disable_percent: 101", "max_disable_percent"},
		"managed in map":    {"password: ${G2I_TEST_PASS}", "password: ${G2I_TEST_PASS}\nsync:\n  default_groups: [google2ipa-managed]", "managed_group"},
		"mail without smtp": {"password: ${G2I_TEST_PASS}", "password: ${G2I_TEST_PASS}\nnotify:\n  admin: {enabled: true, to: [a@b.c]}", "smtp.host"},
		"bad duration":      {"password: ${G2I_TEST_PASS}", "password: ${G2I_TEST_PASS}\noffboarding:\n  delete_after: 3w", "delete_after"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, strings.Replace(minimal, c.from, c.to, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"0": 0, "": 0, "30d": 720 * time.Hour, "12h": 12 * time.Hour} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseDuration("-1d"); err == nil {
		t.Error("negative days accepted")
	}
}

func TestEnvValuesStayStrings(t *testing.T) {
	for _, v := range []string{"12345", "true", "a: b # c", "- x", "'q\"", "0x10"} {
		t.Setenv("G2I_TEST_PASS", v)
		cfg, err := Load(write(t, minimal))
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if cfg.FreeIPA.Password != v {
			t.Errorf("password = %q, want %q", cfg.FreeIPA.Password, v)
		}
	}
}

func TestEnvInTypedFields(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "x")
	t.Setenv("G2I_PORT", "2525")
	t.Setenv("G2I_ADOPT", "true")
	body := minimal + "notify:\n  smtp:\n    port: ${G2I_PORT}\nsync:\n  adopt_existing: ${G2I_ADOPT}\n"
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify.SMTP.Port != 2525 || !cfg.Sync.AdoptExisting {
		t.Errorf("port=%d adopt=%v", cfg.Notify.SMTP.Port, cfg.Sync.AdoptExisting)
	}
}

func TestGroupNamesLowercased(t *testing.T) {
	t.Setenv("G2I_TEST_PASS", "x")
	body := minimal + "sync:\n  managed_group: G2I-Managed\n  default_groups: [Staff]\n  group_mapping:\n    dev@example.com: [Admins]\n"
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.ManagedGroup != "g2i-managed" || cfg.Sync.DefaultGroups[0] != "staff" || cfg.Sync.GroupMapping["dev@example.com"][0] != "admins" {
		t.Errorf("not lowercased: %+v", cfg.Sync)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("FREEIPA_PASSWORD", "x")
	if _, err := Load("../../config.example.yaml"); err != nil {
		t.Fatalf("config.example.yaml must load with only FREEIPA_PASSWORD set: %v", err)
	}
}
