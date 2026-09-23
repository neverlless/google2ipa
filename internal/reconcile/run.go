package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
)

type pass struct {
	mu  sync.Mutex
	r   *Report
	log *slog.Logger
}

func (p *pass) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.r.Errors = append(p.r.Errors, err.Error())
	p.log.Error(err.Error())
}

func forEach[T any](n int, items []T, fn func(T)) {
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for _, it := range items {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(it)
		})
	}
	wg.Wait()
}

// RunOnce performs one sync pass. It never returns an error: everything that
// went wrong is in Report.Errors.
func RunOnce(ctx context.Context, cfg *config.Config, src Source, tgt Target, n Notifier, now time.Time, dryRun bool, log *slog.Logger) (r Report) {
	r = Report{DryRun: dryRun}
	p := &pass{r: &r, log: log}
	if !dryRun {
		// Also on early returns: an aborted pass is exactly what admins need to hear about.
		defer func() {
			if err := n.Summary(r); err != nil {
				p.fail(fmt.Errorf("summary mail: %w", err))
			}
		}()
	}

	gusers, err := src.Users(ctx)
	if err != nil {
		p.fail(fmt.Errorf("google: %w; no changes made", err))
		return r
	}
	users, skipped, errs := Resolve(gusers, cfg.Sync.Username, cfg.Sync.ExcludeUsers, cfg.Sync.MaxUsernameLength)
	for _, e := range errs {
		p.fail(e)
	}

	ipa, err := tgt.ManagedUsers()
	if err != nil {
		p.fail(fmt.Errorf("freeipa: %w; no changes made", err))
		return r
	}
	for _, u := range users {
		if _, ok := ipa[u.UID]; ok {
			continue
		}
		got, err := tgt.Lookup(u.UID)
		if err != nil {
			p.fail(fmt.Errorf("freeipa: %w; no changes made", err))
			return r
		}
		if got != nil {
			ipa[u.UID] = *got
		}
	}

	r.Plan = Build(users, skipped, ipa, cfg, now)
	plan := r.Plan
	log.Info("plan", "google_users", len(users), "managed_users", len(ipa), "create", len(plan.Create),
		"adopt", len(plan.Adopt), "enable", len(plan.Enable), "disable", len(plan.Disable), "delete", len(plan.Delete),
		"group_add", len(plan.AddGroups), "group_remove", len(plan.RemoveGroups), "dry_run", dryRun)
	for _, uid := range plan.Unmanaged {
		log.Warn("existing FreeIPA user is not managed by google2ipa; set sync.adopt_existing to take it over", "uid", uid)
	}
	for _, c := range plan.Conflicts {
		p.fail(errors.New(c))
	}
	if plan.Braked {
		p.fail(fmt.Errorf("safety brake: more than %d%% of managed users would be disabled; skipped disable/delete, check the Google source", cfg.Sync.MaxDisablePercent))
	}
	if dryRun {
		for _, u := range plan.Create {
			log.Info("would create", "uid", u.UID, "email", u.Email)
		}
		for _, uid := range plan.Adopt {
			log.Info("would adopt", "uid", uid)
		}
		for _, uid := range plan.Enable {
			log.Info("would enable", "uid", uid)
		}
		for _, uid := range slices.Sorted(maps.Keys(plan.AddGroups)) {
			log.Info("would add to groups", "uid", uid, "groups", plan.AddGroups[uid])
		}
		for _, uid := range slices.Sorted(maps.Keys(plan.RemoveGroups)) {
			log.Info("would remove from groups", "uid", uid, "groups", plan.RemoveGroups[uid])
		}
		for _, uid := range plan.Disable {
			log.Info("would disable", "uid", uid)
		}
		for _, uid := range plan.Delete {
			log.Info("would delete", "uid", uid, "preserve", cfg.Offboarding.Preserve)
		}
		return r
	}

	workers := cfg.Sync.Concurrency
	created := map[string]bool{} // successful creates; failed ones get no group calls
	forEach(workers, plan.Create, func(u User) {
		pw, err := tgt.CreateUser(u)
		if err != nil {
			p.fail(fmt.Errorf("create %s: %w", u.UID, err))
			return
		}
		p.mu.Lock()
		created[u.UID] = true
		p.mu.Unlock()
		log.Info("created", "uid", u.UID)
		if err := n.Welcome(u, pw); err != nil {
			p.fail(fmt.Errorf("welcome mail to %s: %w", u.Email, err))
		}
	})
	forEach(workers, plan.Adopt, func(uid string) {
		if err := tgt.AddToGroups(uid, []string{cfg.Sync.ManagedGroup}); err != nil {
			p.fail(fmt.Errorf("adopt %s: %w", uid, err))
			return
		}
		log.Info("adopted", "uid", uid)
	})
	forEach(workers, plan.Enable, func(uid string) {
		if err := tgt.Enable(uid); err != nil {
			p.fail(fmt.Errorf("enable %s: %w", uid, err))
			return
		}
		log.Info("enabled", "uid", uid)
	})
	forEach(workers, slices.Sorted(maps.Keys(plan.AddGroups)), func(uid string) {
		if slices.ContainsFunc(plan.Create, func(u User) bool { return u.UID == uid }) && !created[uid] {
			return
		}
		if err := tgt.AddToGroups(uid, plan.AddGroups[uid]); err != nil {
			p.fail(fmt.Errorf("groups %s: %w", uid, err))
		}
	})
	forEach(workers, slices.Sorted(maps.Keys(plan.RemoveGroups)), func(uid string) {
		if err := tgt.RemoveFromGroups(uid, plan.RemoveGroups[uid]); err != nil {
			p.fail(fmt.Errorf("groups %s: %w", uid, err))
		}
	})
	forEach(workers, plan.Disable, func(uid string) {
		if err := tgt.Disable(uid, now); err != nil {
			p.fail(fmt.Errorf("disable %s: %w", uid, err))
			return
		}
		log.Info("disabled", "uid", uid)
	})
	forEach(workers, plan.Delete, func(uid string) {
		if err := tgt.Delete(uid, cfg.Offboarding.Preserve); err != nil {
			p.fail(fmt.Errorf("delete %s: %w", uid, err))
			return
		}
		log.Info("deleted", "uid", uid, "preserved", cfg.Offboarding.Preserve)
	})

	return r
}
