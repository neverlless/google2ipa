package reconcile

import (
	"slices"
	"testing"
)

func TestResolve(t *testing.T) {
	in := []GoogleUser{
		{Email: "John.Doe@Example.com"},
		{Email: "jane@example.com"},
		{Email: "jane@other.org"},      // collides with jane@example.com in local_part mode
		{Email: "bad+tag@example.com"}, // '+' is invalid in a uid
		{Email: "admin@example.com"},   // excluded
	}
	got, skipped, errs := Resolve(in, "local_part", []string{"admin"})
	if len(got) != 1 || got[0].UID != "john.doe" {
		t.Fatalf("resolved = %+v", got)
	}
	if !slices.Equal(skipped, []string{"bad+tag", "jane"}) {
		t.Errorf("skipped = %v", skipped)
	}
	if len(errs) != 2 {
		t.Errorf("errs = %v", errs)
	}

	got, skipped, _ = Resolve(in, "email", []string{"admin.example.com"})
	uids := []string{}
	for _, u := range got {
		uids = append(uids, u.UID)
	}
	if !slices.Equal(uids, []string{"jane.example.com", "jane.other.org", "john.doe.example.com"}) || !slices.Equal(skipped, []string{"bad+tag.example.com"}) {
		t.Errorf("email mode: %v skipped %v", uids, skipped)
	}
}
