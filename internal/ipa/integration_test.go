//go:build integration

package ipa

import (
	"os"
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
	cl, err := Connect(cfg, "google2ipa-managed")
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
