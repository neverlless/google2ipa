// Package reconcile computes and applies the changes that bring FreeIPA in
// line with Google Workspace.
package reconcile

import (
	"context"
	"time"
)

// GoogleUser is an active Google Workspace user after source-side filtering.
type GoogleUser struct {
	Email      string
	GivenName  string
	FamilyName string
	FullName   string
	Groups     []string // lowercased emails of mapped Google groups
}

// User is a Google user with its derived FreeIPA uid.
type User struct {
	UID string
	GoogleUser
}

// IPAUser is the part of a FreeIPA user google2ipa cares about.
type IPAUser struct {
	UID       string
	Managed   bool       // member of sync.managed_group
	Locked    bool       // nsaccountlock
	LockedAt  *time.Time // krbPrincipalExpiration, set by google2ipa on disable
	Email     string     // first mail value, used to detect uid reuse
	Preserved bool       // deleted with --preserve; only an admin can restore it
	Groups    []string
}

type Plan struct {
	Create       []User
	Adopt        []string // existing unmanaged users taken over (adopt_existing)
	Unmanaged    []string // existing unmanaged users left alone
	Enable       []string
	Disable      []string
	Delete       []string
	AddGroups    map[string][]string // uid -> groups
	RemoveGroups map[string][]string
	Braked       bool     // disable/delete suppressed by the safety brake
	Conflicts    []string // uid matches but the FreeIPA mail belongs to someone else; left alone
}

func (p Plan) Empty() bool {
	return len(p.Create)+len(p.Adopt)+len(p.Enable)+len(p.Disable)+len(p.Delete)+
		len(p.AddGroups)+len(p.RemoveGroups) == 0 && !p.Braked
}

// Source provides the active Google users.
type Source interface {
	Users(ctx context.Context) ([]GoogleUser, error)
}

// Target is the FreeIPA side.
type Target interface {
	ManagedUsers() (map[string]IPAUser, error)
	Lookup(uid string) (*IPAUser, error)            // nil, nil when the user does not exist
	CreateUser(u User) (password string, err error) // also adds the managed group
	AddToGroups(uid string, groups []string) error
	RemoveFromGroups(uid string, groups []string) error
	Disable(uid string, at time.Time) error
	Enable(uid string) error
	Delete(uid string, preserve bool) error
}

// Notifier sends mail; implementations decide whether a kind is enabled.
type Notifier interface {
	Welcome(u User, password string) error
	Summary(r Report) error
}

// Report is the outcome of one pass.
type Report struct {
	DryRun bool
	Plan   Plan
	Errors []string
}

func (r Report) OK() bool { return len(r.Errors) == 0 }
