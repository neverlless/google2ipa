package reconcile

import (
	"slices"
	"testing"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
)

var now = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func cfg(mut func(*config.Config)) *config.Config {
	c := config.Default()
	c.Sync.DefaultGroups = []string{"staff"}
	c.Sync.GroupMapping = map[string][]string{"dev@example.com": {"developers"}, "ops@example.com": {"admins", "ops"}}
	c.Sync.MaxDisablePercent = 0
	if mut != nil {
		mut(c)
	}
	return c
}

func u(uid string, groups ...string) User {
	return User{UID: uid, GoogleUser: GoogleUser{Email: uid + "@example.com", Groups: groups}}
}

func TestBuildLifecycle(t *testing.T) {
	ipa := map[string]IPAUser{
		"active":   {UID: "active", Managed: true, Groups: []string{"staff"}},
		"gone":     {UID: "gone", Managed: true},
		"back":     {UID: "back", Managed: true, Locked: true, LockedAt: ago(time.Hour), Groups: []string{"staff"}},
		"manual":   {UID: "manual", Managed: true, Locked: true, Groups: []string{"staff"}}, // locked by admin
		"old":      {UID: "old", Managed: true, Locked: true, LockedAt: ago(31 * 24 * time.Hour)},
		"recent":   {UID: "recent", Managed: true, Locked: true, LockedAt: ago(24 * time.Hour)},
		"legacy":   {UID: "legacy", Managed: false},
		"skipme":   {UID: "skipme", Managed: true},
		"excluded": {UID: "excluded", Managed: true},
	}
	users := []User{u("active"), u("back"), u("legacy"), u("manual"), u("new")}
	p := Build(users, []string{"skipme"}, ipa, cfg(func(c *config.Config) { c.Sync.ExcludeUsers = []string{"excluded"} }), now)

	check := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if len(p.Create) != 1 || p.Create[0].UID != "new" {
		t.Errorf("Create = %+v", p.Create)
	}
	check("Enable", p.Enable, []string{"back"})
	check("Disable", p.Disable, []string{"gone"})
	check("Delete", p.Delete, []string{"old"})
	check("Unmanaged", p.Unmanaged, []string{"legacy"})
	check("Adopt", p.Adopt, nil)
	check("AddGroups[new]", p.AddGroups["new"], []string{"staff"})
	if _, ok := p.AddGroups["legacy"]; ok {
		t.Error("unmanaged user must not get group changes")
	}
}

func TestBuildAdopt(t *testing.T) {
	ipa := map[string]IPAUser{"legacy": {UID: "legacy"}}
	p := Build([]User{u("legacy")}, nil, ipa, cfg(func(c *config.Config) { c.Sync.AdoptExisting = true }), now)
	if !slices.Equal(p.Adopt, []string{"legacy"}) || !slices.Equal(p.AddGroups["legacy"], []string{"staff"}) {
		t.Errorf("adopt: %+v", p)
	}
}

func TestBuildGroups(t *testing.T) {
	ipa := map[string]IPAUser{
		"x": {UID: "x", Managed: true, Groups: []string{"staff", "admins", "ops", "manual-group"}},
	}
	p := Build([]User{u("x", "dev@example.com")}, nil, ipa, cfg(nil), now)
	if !slices.Equal(p.AddGroups["x"], []string{"developers"}) {
		t.Errorf("add = %v", p.AddGroups["x"])
	}
	// only mapped groups are removed; staff (default) and manual-group are kept
	if !slices.Equal(p.RemoveGroups["x"], []string{"admins", "ops"}) {
		t.Errorf("remove = %v", p.RemoveGroups["x"])
	}
}

func TestBuildDisableOffAndNeverDelete(t *testing.T) {
	ipa := map[string]IPAUser{
		"gone": {UID: "gone", Managed: true},
		"old":  {UID: "old", Managed: true, Locked: true, LockedAt: ago(365 * 24 * time.Hour)},
	}
	p := Build(nil, nil, ipa, cfg(func(c *config.Config) { c.Offboarding.Disable = false; c.Offboarding.DeleteAfter = 0 }), now)
	if len(p.Disable)+len(p.Delete) != 0 {
		t.Errorf("got %+v", p)
	}
}

func TestBuildSafetyBrake(t *testing.T) {
	ipa := map[string]IPAUser{}
	var users []User
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		ipa[id] = IPAUser{UID: id, Managed: true}
	}
	users = append(users, u("a"), u("b"), u("c"), u("d"), u("e"), u("f"), u("g"), u("h"))
	ipa["old"] = IPAUser{UID: "old", Managed: true, Locked: true, LockedAt: ago(90 * 24 * time.Hour)}

	// 2 of 10 active = 20% -> allowed at 20
	p := Build(users, nil, ipa, cfg(func(c *config.Config) { c.Sync.MaxDisablePercent = 20 }), now)
	if p.Braked || len(p.Disable) != 2 || len(p.Delete) != 1 {
		t.Fatalf("20%%: %+v", p)
	}
	// 3 of 10 = 30% -> braked, delete suppressed too
	p = Build(users[:7], nil, ipa, cfg(func(c *config.Config) { c.Sync.MaxDisablePercent = 20 }), now)
	if !p.Braked || p.Disable != nil || p.Delete != nil {
		t.Fatalf("30%%: %+v", p)
	}
	// a single disable never trips the brake
	small := map[string]IPAUser{"a": {UID: "a", Managed: true}, "b": {UID: "b", Managed: true}}
	p = Build([]User{u("a")}, nil, small, cfg(func(c *config.Config) { c.Sync.MaxDisablePercent = 20 }), now)
	if p.Braked || len(p.Disable) != 1 {
		t.Fatalf("single: %+v", p)
	}
}
