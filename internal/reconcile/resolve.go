package reconcile

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// FreeIPA's default uid pattern, lowercase only.
var uidPattern = regexp.MustCompile(`^[a-z0-9_.][a-z0-9_.-]{0,252}[a-z0-9_.$-]?$`)

func deriveUID(email, mode string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if mode == "email" {
		return strings.Replace(email, "@", ".", 1)
	}
	local, _, _ := strings.Cut(email, "@")
	return local
}

// Resolve derives uids. Invalid or colliding uids are returned in skipped:
// they count as present in Google but are never modified.
func Resolve(users []GoogleUser, mode string, exclude []string) (resolved []User, skipped []string, errs []error) {
	byUID := map[string][]GoogleUser{}
	for _, u := range users {
		uid := deriveUID(u.Email, mode)
		if slices.Contains(exclude, uid) {
			continue
		}
		byUID[uid] = append(byUID[uid], u)
	}
	uids := make([]string, 0, len(byUID))
	for uid := range byUID {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	for _, uid := range uids {
		us := byUID[uid]
		switch {
		case !uidPattern.MatchString(uid):
			skipped = append(skipped, uid)
			errs = append(errs, fmt.Errorf("skip %s: %q is not a valid FreeIPA uid", us[0].Email, uid))
		case len(us) > 1:
			skipped = append(skipped, uid)
			emails := make([]string, len(us))
			for i, u := range us {
				emails[i] = u.Email
			}
			errs = append(errs, fmt.Errorf("skip uid %q: derived from several Google users (%s)", uid, strings.Join(emails, ", ")))
		default:
			resolved = append(resolved, User{UID: uid, GoogleUser: us[0]})
		}
	}
	return resolved, skipped, errs
}
