package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neverlless/google2ipa/internal/config"
)

type fakeSrc struct {
	users []GoogleUser
	err   error
}

func (f fakeSrc) Users(context.Context) ([]GoogleUser, error) { return f.users, f.err }

type fakeTgt struct {
	mu       sync.Mutex
	managed  map[string]IPAUser
	existing map[string]IPAUser
	failOn   map[string]bool // "op:uid"
	calls    []string
}

func (f *fakeTgt) rec(op, uid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+":"+uid)
	if f.failOn[op+":"+uid] {
		return errors.New("boom")
	}
	return nil
}
func (f *fakeTgt) ManagedUsers() (map[string]IPAUser, error) { return f.managed, nil }
func (f *fakeTgt) Lookup(uid string) (*IPAUser, error) {
	if u, ok := f.existing[uid]; ok {
		return &u, nil
	}
	return nil, nil
}
func (f *fakeTgt) CreateUser(u User) (string, error) { return "pw-" + u.UID, f.rec("create", u.UID) }
func (f *fakeTgt) AddToGroups(uid string, g []string) error {
	return f.rec("add["+strings.Join(g, ",")+"]", uid)
}
func (f *fakeTgt) RemoveFromGroups(uid string, g []string) error {
	return f.rec("remove["+strings.Join(g, ",")+"]", uid)
}
func (f *fakeTgt) Disable(uid string, _ time.Time) error { return f.rec("disable", uid) }
func (f *fakeTgt) Enable(uid string) error               { return f.rec("enable", uid) }
func (f *fakeTgt) Delete(uid string, _ bool) error       { return f.rec("delete", uid) }

type fakeNotifier struct {
	mu       sync.Mutex
	welcomed []string
	reports  []Report
}

func (n *fakeNotifier) Welcome(u User, pw string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.welcomed = append(n.welcomed, u.UID+"/"+pw)
	return nil
}
func (n *fakeNotifier) Summary(r Report) error { n.reports = append(n.reports, r); return nil }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func gu(email string) GoogleUser { return GoogleUser{Email: email} }

func TestRunOnceApplies(t *testing.T) {
	c := cfg(nil)
	tgt := &fakeTgt{
		managed:  map[string]IPAUser{"gone": {UID: "gone", Managed: true}, "stay": {UID: "stay", Managed: true, Groups: []string{"staff"}}},
		existing: map[string]IPAUser{},
		failOn:   map[string]bool{"create:bad": true},
	}
	n := &fakeNotifier{}
	src := fakeSrc{users: []GoogleUser{gu("new@example.com"), gu("bad@example.com"), gu("stay@example.com")}}
	r := RunOnce(context.Background(), c, src, tgt, n, now, false, quiet)

	slices.Sort(tgt.calls)
	want := []string{"add[staff]:new", "create:bad", "create:new", "disable:gone"}
	if !slices.Equal(tgt.calls, want) {
		t.Errorf("calls = %v, want %v", tgt.calls, want)
	}
	if !slices.Equal(n.welcomed, []string{"new/pw-new"}) {
		t.Errorf("welcomed = %v", n.welcomed)
	}
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "bad") || r.OK() {
		t.Errorf("errors = %v", r.Errors)
	}
	if len(n.reports) != 1 {
		t.Errorf("summary sent %d times", len(n.reports))
	}
}

func TestRunOnceGoogleErrorChangesNothing(t *testing.T) {
	tgt := &fakeTgt{managed: map[string]IPAUser{"a": {UID: "a", Managed: true}}}
	r := RunOnce(context.Background(), cfg(nil), fakeSrc{err: errors.New("403")}, tgt, &fakeNotifier{}, now, false, quiet)
	if r.OK() || len(tgt.calls) != 0 {
		t.Errorf("r=%+v calls=%v", r, tgt.calls)
	}
}

func TestRunOnceDryRun(t *testing.T) {
	tgt := &fakeTgt{managed: map[string]IPAUser{"gone": {UID: "gone", Managed: true}}, existing: map[string]IPAUser{}}
	n := &fakeNotifier{}
	r := RunOnce(context.Background(), cfg(nil), fakeSrc{users: []GoogleUser{gu("new@example.com")}}, tgt, n, now, true, quiet)
	if len(tgt.calls) != 0 || len(n.welcomed) != 0 || !r.DryRun || len(r.Plan.Create) != 1 {
		t.Errorf("dry run touched target: %v %+v", tgt.calls, r)
	}
}

func TestRunOnceBrakeIsError(t *testing.T) {
	c := cfg(func(c *config.Config) { c.Sync.MaxDisablePercent = 10 })
	tgt := &fakeTgt{managed: map[string]IPAUser{"a": {UID: "a", Managed: true}, "b": {UID: "b", Managed: true}}, existing: map[string]IPAUser{}}
	r := RunOnce(context.Background(), c, fakeSrc{}, tgt, &fakeNotifier{}, now, false, quiet)
	if r.OK() || !r.Plan.Braked || len(tgt.calls) != 0 {
		t.Errorf("r=%+v calls=%v", r, tgt.calls)
	}
}

func TestRunOnceGroupFailureIsolated(t *testing.T) {
	c := cfg(nil)
	tgt := &fakeTgt{
		managed:  map[string]IPAUser{"a": {UID: "a", Managed: true}, "b": {UID: "b", Managed: true}},
		existing: map[string]IPAUser{},
		failOn:   map[string]bool{"add[staff]:a": true},
	}
	r := RunOnce(context.Background(), c, fakeSrc{users: []GoogleUser{gu("a@example.com"), gu("b@example.com")}}, tgt, &fakeNotifier{}, now, false, quiet)
	if len(r.Errors) != 1 || !slices.Contains(tgt.calls, "add[staff]:b") {
		t.Errorf("errors=%v calls=%v", r.Errors, tgt.calls)
	}
}

func TestRunOnceSummaryOnAbort(t *testing.T) {
	n := &fakeNotifier{}
	RunOnce(context.Background(), cfg(nil), fakeSrc{err: errors.New("403")}, &fakeTgt{}, n, now, false, quiet)
	if len(n.reports) != 1 || n.reports[0].OK() {
		t.Fatalf("admin must be told about an aborted pass: %+v", n.reports)
	}
}

func TestRunOnceConflictIsError(t *testing.T) {
	tgt := &fakeTgt{managed: map[string]IPAUser{"john": {UID: "john", Managed: true, Email: "john@a.com"}}}
	r := RunOnce(context.Background(), cfg(nil), fakeSrc{users: []GoogleUser{gu("john@b.com")}}, tgt, &fakeNotifier{}, now, false, quiet)
	if r.OK() || !strings.Contains(strings.Join(r.Errors, ";"), "john@a.com") || len(tgt.calls) != 0 {
		t.Errorf("errors=%v calls=%v", r.Errors, tgt.calls)
	}
}

func TestDryRunLogsEveryAction(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	tgt := &fakeTgt{managed: map[string]IPAUser{
		"back": {UID: "back", Managed: true, Locked: true, LockedAt: ago(time.Hour), Groups: []string{"staff", "admins"}},
	}}
	n := &fakeNotifier{}
	RunOnce(context.Background(), cfg(nil), fakeSrc{users: []GoogleUser{gu("back@example.com")}}, tgt, n, now, true, log)
	out := buf.String()
	for _, want := range []string{"would enable", "would remove from groups", "admins"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run log missing %q:\n%s", want, out)
		}
	}
	if len(n.reports) != 0 {
		t.Error("dry run must not mail")
	}
}

type failingNotifier struct{ fakeNotifier }

func (failingNotifier) Summary(Report) error { return errors.New("smtp down") }

func TestSummaryFailureIsReported(t *testing.T) {
	tgt := &fakeTgt{managed: map[string]IPAUser{}, existing: map[string]IPAUser{}}
	r := RunOnce(context.Background(), cfg(nil), fakeSrc{}, tgt, &failingNotifier{}, now, false, quiet)
	if r.OK() {
		t.Error("summary mail failure must be reported")
	}
}
