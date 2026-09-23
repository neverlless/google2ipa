// Package ipa is the FreeIPA side of google2ipa, over the JSON-RPC API.
package ipa

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ccin2p3/go-freeipa/freeipa"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

const genTime = "20060102150405Z" // LDAP generalized time

type Client struct {
	c       *freeipa.Client
	managed string
}

// backoff is a variable so tests can make retries instant.
var backoff = func(attempt int) time.Duration { return time.Second << (2 * attempt) } // 1s, 4s

func apiErr(err error, code int) bool {
	var e *freeipa.Error
	return errors.As(err, &e) && (code == 0 || e.Code == code)
}

// retry retries only transport-level failures, never FreeIPA API errors.
func retry[T any](fn func() (T, error)) (T, error) {
	var v T
	var err error
	for i := range 3 {
		if v, err = fn(); err == nil || apiErr(err, 0) {
			return v, err
		}
		if i < 2 {
			time.Sleep(backoff(i))
		}
	}
	return v, err
}

func transport(cfg config.FreeIPA) (*http.Transport, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", cfg.CAFile)
		}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // explicit opt-in for lab setups
	}
	t.ResponseHeaderTimeout = time.Minute
	return t, nil
}

// Connect logs in and checks that the managed group exists.
func Connect(cfg config.FreeIPA, managedGroup string) (*Client, error) {
	tr, err := transport(cfg)
	if err != nil {
		return nil, fmt.Errorf("freeipa tls: %w", err)
	}
	c, err := retry(func() (*freeipa.Client, error) {
		return freeipa.Connect(cfg.Host(), tr, cfg.Username, cfg.Password)
	})
	if err != nil {
		return nil, fmt.Errorf("freeipa login to %s: %w", cfg.Host(), err)
	}
	_, err = retry(func() (*freeipa.GroupShowResult, error) {
		return c.GroupShow(&freeipa.GroupShowArgs{Cn: managedGroup}, &freeipa.GroupShowOptionalArgs{})
	})
	if apiErr(err, freeipa.NotFoundCode) {
		return nil, fmt.Errorf("managed group %q does not exist; create it with: ipa group-add %s --desc='Users managed by google2ipa'", managedGroup, managedGroup)
	}
	if err != nil {
		return nil, fmt.Errorf("freeipa group_show %s: %w", managedGroup, err)
	}
	return &Client{c: c, managed: managedGroup}, nil
}

func (cl *Client) toIPAUser(u freeipa.User) reconcile.IPAUser {
	out := reconcile.IPAUser{UID: u.UID, LockedAt: u.Krbprincipalexpiration}
	if u.Nsaccountlock != nil {
		out.Locked = *u.Nsaccountlock
	}
	if u.MemberofGroup != nil {
		out.Groups = *u.MemberofGroup
	}
	for _, g := range out.Groups {
		if g == cl.managed {
			out.Managed = true
		}
	}
	return out
}

func (cl *Client) ManagedUsers() (map[string]reconcile.IPAUser, error) {
	res, err := retry(func() (*freeipa.UserFindResult, error) {
		return cl.c.UserFind("", &freeipa.UserFindArgs{}, &freeipa.UserFindOptionalArgs{
			InGroup:   &[]string{cl.managed},
			Sizelimit: freeipa.Int(0),
			All:       freeipa.Bool(true),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("list managed users: %w", err)
	}
	if res.Truncated {
		return nil, errors.New("list managed users: result truncated by the FreeIPA search size limit; raise it with ipa config-mod --searchrecordslimit")
	}
	out := make(map[string]reconcile.IPAUser, len(res.Result))
	for _, u := range res.Result {
		out[u.UID] = cl.toIPAUser(u)
	}
	return out, nil
}

func (cl *Client) Lookup(uid string) (*reconcile.IPAUser, error) {
	res, err := retry(func() (*freeipa.UserShowResult, error) {
		return cl.c.UserShow(&freeipa.UserShowArgs{}, &freeipa.UserShowOptionalArgs{UID: &uid, All: freeipa.Bool(true)})
	})
	if apiErr(err, freeipa.NotFoundCode) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("user_show %s: %w", uid, err)
	}
	u := cl.toIPAUser(res.Result)
	return &u, nil
}

func orUID(v, uid string) string {
	if strings.TrimSpace(v) == "" {
		return uid
	}
	return v
}

func (cl *Client) CreateUser(u reconcile.User) (string, error) {
	given, family := orUID(u.GivenName, u.UID), orUID(u.FamilyName, u.UID)
	full := orUID(u.FullName, given+" "+family)
	res, err := cl.c.UserAdd(&freeipa.UserAddArgs{Givenname: given, Sn: family}, &freeipa.UserAddOptionalArgs{
		UID:         &u.UID,
		Cn:          &full,
		Displayname: &full,
		Mail:        &[]string{u.Email},
		Random:      freeipa.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("user_add: %w", err)
	}
	if err := cl.AddToGroups(u.UID, []string{cl.managed}); err != nil {
		return "", fmt.Errorf("user created but not added to managed group, add it manually: %w", err)
	}
	if res.Result.Randompassword == nil {
		return "", nil
	}
	return *res.Result.Randompassword, nil
}

// failures returns member failures other than the ignorable reason.
func failures(f freeipa.FailedOperations, ignore string) error {
	var msgs []string
	for kind, ops := range f.GetFailures() {
		for _, op := range ops {
			if op.Reason != ignore {
				msgs = append(msgs, fmt.Sprintf("%s %s: %s", kind, op.Name, op.Reason))
			}
		}
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

func (cl *Client) AddToGroups(uid string, groups []string) error {
	var errs []error
	for _, g := range groups {
		res, err := cl.c.GroupAddMember(&freeipa.GroupAddMemberArgs{Cn: g}, &freeipa.GroupAddMemberOptionalArgs{User: &[]string{uid}})
		if err == nil {
			err = failures(res.Failed, freeipa.FailedReasonAlreadyAMember)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("add to group %s: %w", g, err))
		}
	}
	return errors.Join(errs...)
}

func (cl *Client) RemoveFromGroups(uid string, groups []string) error {
	var errs []error
	for _, g := range groups {
		res, err := cl.c.GroupRemoveMember(&freeipa.GroupRemoveMemberArgs{Cn: g}, &freeipa.GroupRemoveMemberOptionalArgs{User: &[]string{uid}})
		if err == nil {
			err = failures(res.Failed, "This entry is not a member")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove from group %s: %w", g, err))
		}
	}
	return errors.Join(errs...)
}

func (cl *Client) setPrincipalExpiration(uid, value string) error {
	_, err := cl.c.UserMod(&freeipa.UserModArgs{}, &freeipa.UserModOptionalArgs{
		UID:     &uid,
		Setattr: &[]string{"krbprincipalexpiration=" + value},
	})
	if apiErr(err, freeipa.EmptyModlistCode) {
		return nil
	}
	return err
}

// Disable stamps the lock time first, so a failed disable is retried next pass
// with the stamp in place.
func (cl *Client) Disable(uid string, at time.Time) error {
	if err := cl.setPrincipalExpiration(uid, at.UTC().Format(genTime)); err != nil {
		return fmt.Errorf("set principal expiration: %w", err)
	}
	_, err := cl.c.UserDisable(&freeipa.UserDisableArgs{}, &freeipa.UserDisableOptionalArgs{UID: &uid})
	if err != nil && !apiErr(err, freeipa.AlreadyInactiveCode) {
		return fmt.Errorf("user_disable: %w", err)
	}
	return nil
}

func (cl *Client) Enable(uid string) error {
	_, err := cl.c.UserEnable(&freeipa.UserEnableArgs{}, &freeipa.UserEnableOptionalArgs{UID: &uid})
	if err != nil && !apiErr(err, freeipa.AlreadyActiveCode) {
		return fmt.Errorf("user_enable: %w", err)
	}
	if err := cl.setPrincipalExpiration(uid, ""); err != nil {
		return fmt.Errorf("enabled, but clearing principal expiration failed (Kerberos stays blocked until cleared): %w", err)
	}
	return nil
}

func (cl *Client) Delete(uid string, preserve bool) error {
	_, err := cl.c.UserDel(&freeipa.UserDelArgs{}, &freeipa.UserDelOptionalArgs{UID: &[]string{uid}, Preserve: &preserve})
	if err != nil {
		return fmt.Errorf("user_del: %w", err)
	}
	return nil
}
