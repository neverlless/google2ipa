package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	admin "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/option"

	"github.com/neverlless/google2ipa/internal/config"
)

func TestUsers(t *testing.T) {
	pages := map[string]map[string]any{
		"": {"nextPageToken": "p2", "users": []map[string]any{
			{"primaryEmail": "John@Example.com", "orgUnitPath": "/Eng", "name": map[string]string{"givenName": "John", "familyName": "Doe", "fullName": "John Doe"}},
			{"primaryEmail": "sus@example.com", "orgUnitPath": "/Eng", "suspended": true},
			{"primaryEmail": "arch@example.com", "orgUnitPath": "/Eng", "archived": true},
		}},
		"p2": {"users": []map[string]any{
			{"primaryEmail": "noname@example.com", "orgUnitPath": "/Eng/Platform"},
			{"primaryEmail": "sales@example.com", "orgUnitPath": "/Sales"},
			{"primaryEmail": "x@other.org", "orgUnitPath": "/Eng"},
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/users"):
			if r.URL.Query().Get("customer") != "my_customer" || r.URL.Query().Get("maxResults") != "500" {
				t.Errorf("query = %v", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(pages[r.URL.Query().Get("pageToken")])
		case strings.HasSuffix(r.URL.Path, "/groups/devops@example.com/members"):
			if r.URL.Query().Get("includeDerivedMembership") != "true" {
				t.Error("nested membership not requested")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"members": []map[string]string{
				{"email": "JOHN@example.com", "type": "USER"},
				{"email": "nested@example.com", "type": "GROUP"},
			}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc, err := admin.NewService(context.Background(), option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Google{Customer: "my_customer", OrgUnits: []string{"/eng"}, Domains: []string{"example.com"}}
	src := &Source{svc: svc, cfg: cfg, groups: []string{"devops@example.com"}}

	users, err := src.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var emails []string
	for _, u := range users {
		emails = append(emails, u.Email)
	}
	if !slices.Equal(emails, []string{"john@example.com", "noname@example.com"}) {
		t.Fatalf("emails = %v", emails)
	}
	if !slices.Equal(users[0].Groups, []string{"devops@example.com"}) || users[0].GivenName != "John" {
		t.Errorf("john = %+v", users[0])
	}
}
