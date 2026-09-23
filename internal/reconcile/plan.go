package reconcile

import (
	"maps"
	"slices"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
)

// Build is a pure function: same inputs, same plan. It never performs I/O.
func Build(users []User, skipped []string, ipa map[string]IPAUser, cfg *config.Config, now time.Time) Plan {
	p := Plan{AddGroups: map[string][]string{}, RemoveGroups: map[string][]string{}}
	excluded := cfg.Sync.ExcludeUsers

	var mapped []string
	for _, gs := range cfg.Sync.GroupMapping {
		mapped = append(mapped, gs...)
	}

	inGoogle := map[string]bool{}
	for _, uid := range skipped {
		inGoogle[uid] = true
	}
	for _, u := range users {
		inGoogle[u.UID] = true
		if slices.Contains(excluded, u.UID) {
			continue
		}
		cur, exists := ipa[u.UID]
		switch {
		case !exists:
			p.Create = append(p.Create, u)
		case !cur.Managed && !cfg.Sync.AdoptExisting:
			p.Unmanaged = append(p.Unmanaged, u.UID)
			continue
		case !cur.Managed:
			p.Adopt = append(p.Adopt, u.UID)
		case cur.Locked && cur.LockedAt != nil:
			p.Enable = append(p.Enable, u.UID)
		}

		want := slices.Clone(cfg.Sync.DefaultGroups)
		for _, g := range u.Groups {
			want = append(want, cfg.Sync.GroupMapping[g]...)
		}
		var add, remove []string
		for _, g := range want {
			if !slices.Contains(cur.Groups, g) && !slices.Contains(add, g) {
				add = append(add, g)
			}
		}
		for _, g := range cur.Groups {
			if slices.Contains(mapped, g) && !slices.Contains(want, g) && !slices.Contains(remove, g) {
				remove = append(remove, g)
			}
		}
		slices.Sort(add)
		slices.Sort(remove)
		if len(add) > 0 {
			p.AddGroups[u.UID] = add
		}
		if len(remove) > 0 {
			p.RemoveGroups[u.UID] = remove
		}
	}

	deleteAfter := time.Duration(cfg.Offboarding.DeleteAfter)
	active := 0
	for _, uid := range slices.Sorted(maps.Keys(ipa)) {
		cur := ipa[uid]
		if !cur.Managed || slices.Contains(excluded, uid) {
			continue
		}
		if !cur.Locked {
			active++
		}
		if inGoogle[uid] {
			continue
		}
		switch {
		case !cur.Locked && cfg.Offboarding.Disable:
			p.Disable = append(p.Disable, uid)
		case cur.Locked && cur.LockedAt != nil && deleteAfter > 0 && now.Sub(*cur.LockedAt) >= deleteAfter:
			p.Delete = append(p.Delete, uid)
		}
	}

	if pct := cfg.Sync.MaxDisablePercent; pct > 0 && len(p.Disable) > 1 && len(p.Disable)*100 > pct*active {
		p.Braked, p.Disable, p.Delete = true, nil, nil
	}
	return p
}
