//go:build integration

package ipa

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

func TestIntegrationLifecycle(t *testing.T) {
	cfg := config.FreeIPA{URL: os.Getenv("G2I_IT_URL"), Username: os.Getenv("G2I_IT_USER"),
		Password: os.Getenv("G2I_IT_PASSWORD"), CAFile: os.Getenv("G2I_IT_CA")}
	if cfg.URL == "" {
		t.Skip("G2I_IT_URL not set; run hack/freeipa-up.sh")
	}
	cl, err := Connect(context.Background(), cfg, "google2ipa-managed")
	if err != nil {
		t.Fatal(err)
	}
	uid := "it" + time.Now().Format("150405")
	if got, err := cl.Lookup(uid); err != nil || got != nil {
		t.Fatalf("lookup before create: %v %v", got, err)
	}
	pw, err := cl.CreateUser(reconcile.User{UID: uid, GoogleUser: reconcile.GoogleUser{
		Email: uid + "@example.test", GivenName: "Int", FamilyName: "Test", FullName: "Int Test"}})
	if err != nil || pw == "" {
		t.Fatalf("create: pw=%q err=%v", pw, err)
	}
	if err := cl.AddToGroups(uid, []string{"g2i-staff"}); err != nil {
		t.Fatal(err)
	}
	if err := cl.AddToGroups(uid, []string{"g2i-staff"}); err != nil {
		t.Fatalf("second add must be a no-op: %v", err)
	}

	users, err := cl.ManagedUsers()
	if err != nil {
		t.Fatal(err)
	}
	u, ok := users[uid]
	if !ok || u.Locked || u.LockedAt != nil {
		t.Fatalf("managed user = %+v ok=%v", u, ok)
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := cl.Disable(uid, at); err != nil {
		t.Fatal(err)
	}
	users, _ = cl.ManagedUsers()
	if u = users[uid]; !u.Locked || u.LockedAt == nil || !u.LockedAt.Equal(at) {
		t.Fatalf("after disable = %+v (want LockedAt %v)", u, at)
	}
	if err := cl.Enable(uid); err != nil {
		t.Fatal(err)
	}
	users, _ = cl.ManagedUsers()
	if u = users[uid]; u.Locked || u.LockedAt != nil {
		t.Fatalf("after enable = %+v", u)
	}
	if err := cl.RemoveFromGroups(uid, []string{"g2i-staff"}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(uid, true); err != nil {
		t.Fatal(err)
	}
	// user_show still finds preserved users: locked and no longer managed,
	// so the planner leaves them alone until an admin runs ipa user-undel.
	if got, err := cl.Lookup(uid); err != nil || (got != nil && (got.Managed || !got.Locked)) {
		t.Fatalf("after preserve-delete user must be gone or locked+unmanaged: %+v %v", got, err)
	}
}

type itSource []reconcile.GoogleUser

func (s itSource) Users(context.Context) ([]reconcile.GoogleUser, error) { return s, nil }

type itNotifier struct{ passwords map[string]string }

func (n *itNotifier) Welcome(u reconcile.User, pw string) error { n.passwords[u.UID] = pw; return nil }
func (n *itNotifier) Summary(reconcile.Report) error            { return nil }

// TestIntegrationFullPass drives complete sync passes with a fake Google source.
func TestIntegrationFullPass(t *testing.T) {
	ipaCfg := config.FreeIPA{URL: os.Getenv("G2I_IT_URL"), Username: os.Getenv("G2I_IT_USER"),
		Password: os.Getenv("G2I_IT_PASSWORD"), CAFile: os.Getenv("G2I_IT_CA")}
	if ipaCfg.URL == "" {
		t.Skip("G2I_IT_URL not set; run hack/freeipa-up.sh")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.FreeIPA = ipaCfg
	cfg.Sync.DefaultGroups = []string{"g2i-staff"}
	cfg.Sync.MaxDisablePercent = 0
	cfg.Offboarding.DeleteAfter = config.Duration(time.Minute)

	uid := "fp" + time.Now().Format("150405")
	alice := itSource{{Email: uid + "@example.test", GivenName: "Alice", FamilyName: "Pass"}}
	n := &itNotifier{passwords: map[string]string{}}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pass := func(src reconcile.Source, now time.Time) map[string]reconcile.IPAUser {
		t.Helper()
		cl, err := Connect(ctx, ipaCfg, cfg.Sync.ManagedGroup)
		if err != nil {
			t.Fatal(err)
		}
		if r := reconcile.RunOnce(ctx, cfg, src, cl, n, now, false, log); !r.OK() {
			t.Fatalf("pass errors: %v", r.Errors)
		}
		users, err := cl.ManagedUsers()
		if err != nil {
			t.Fatal(err)
		}
		return users
	}

	now := time.Now().UTC().Truncate(time.Second)
	u := pass(alice, now)[uid]
	if u.UID == "" || u.Locked || !slices.Contains(u.Groups, "g2i-staff") || n.passwords[uid] == "" {
		t.Fatalf("after create: %+v pw=%q", u, n.passwords[uid])
	}
	if u = pass(itSource{}, now)[uid]; !u.Locked || u.LockedAt == nil {
		t.Fatalf("after leaving Google: %+v", u)
	}
	if u = pass(alice, now)[uid]; u.Locked || u.LockedAt != nil {
		t.Fatalf("after coming back: %+v", u)
	}
	pass(itSource{}, now)
	if users := pass(itSource{}, now.Add(time.Hour)); users[uid].UID != "" {
		t.Fatalf("still managed after delete_after: %+v", users[uid])
	}
}
