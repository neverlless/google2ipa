package ipa

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccin2p3/go-freeipa/freeipa"

	"github.com/neverlless/google2ipa/internal/config"
	"github.com/neverlless/google2ipa/internal/reconcile"
)

type call struct {
	Method string
	Args   []any
	Opts   map[string]any
}

type handler func(c call) (result any, ipaErr *freeipa.Error)

// fake starts a TLS FreeIPA stand-in and returns a config pointing at it
// (CA pinned via ca_file) plus the recorded calls.
func fake(t *testing.T, h handler) (config.FreeIPA, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ipa/session/login_password":
			w.WriteHeader(http.StatusOK)
		case "/ipa/session/json":
			var req struct {
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			var c call
			c.Method = req.Method
			_ = json.Unmarshal(req.Params[0], &c.Args)
			_ = json.Unmarshal(req.Params[1], &c.Opts)
			mu.Lock()
			calls = append(calls, c)
			mu.Unlock()
			res, ipaErr := h(c)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": res, "error": ipaErr, "id": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.FreeIPA{URL: srv.URL, Username: "svc", Password: "pw", CAFile: ca}, func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

func notFound() *freeipa.Error {
	return &freeipa.Error{Code: freeipa.NotFoundCode, Name: "NotFound", Message: "not found"}
}

func groupOK(c call) (any, *freeipa.Error) {
	if c.Method == "group_show" {
		return map[string]any{"result": map[string]any{"cn": []string{"google2ipa-managed"}}, "value": "google2ipa-managed"}, nil
	}
	return nil, nil
}

func TestConnectMissingManagedGroup(t *testing.T) {
	cfg, _ := fake(t, func(c call) (any, *freeipa.Error) { return nil, notFound() })
	_, err := Connect(cfg, "google2ipa-managed")
	if err == nil || !strings.Contains(err.Error(), "ipa group-add google2ipa-managed") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagedUsersAndLookup(t *testing.T) {
	cfg, _ := fake(t, func(c call) (any, *freeipa.Error) {
		switch c.Method {
		case "user_find":
			return map[string]any{"count": 1, "truncated": false, "result": []any{map[string]any{
				"uid": []string{"john"}, "givenname": []string{"John"}, "sn": []string{"Doe"}, "mail": []string{"John@Example.com"},
				"nsaccountlock":          true,
				"krbprincipalexpiration": []any{map[string]string{"__datetime__": "20260901000000Z"}},
				"memberof_group":         []string{"google2ipa-managed", "staff"},
			}}}, nil
		case "user_show":
			if c.Opts["uid"] == "ghost" {
				return nil, notFound()
			}
			return map[string]any{"value": "legacy", "result": map[string]any{"uid": []string{"legacy"}, "sn": []string{"L"}, "memberof_group": []string{"staff"}}}, nil
		}
		return groupOK(c)
	})
	cl, err := Connect(cfg, "google2ipa-managed")
	if err != nil {
		t.Fatal(err)
	}
	users, err := cl.ManagedUsers()
	if err != nil {
		t.Fatal(err)
	}
	j := users["john"]
	if j.Email != "John@Example.com" || !j.Managed || !j.Locked || j.LockedAt == nil || !j.LockedAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("john = %+v", j)
	}
	if got, err := cl.Lookup("ghost"); got != nil || err != nil {
		t.Errorf("ghost = %v, %v", got, err)
	}
	if got, err := cl.Lookup("legacy"); err != nil || got == nil || got.Managed {
		t.Errorf("legacy = %+v, %v", got, err)
	}
}

func TestCreateUser(t *testing.T) {
	cfg, calls := fake(t, func(c call) (any, *freeipa.Error) {
		if c.Method == "user_add" {
			return map[string]any{"value": "nameless", "result": map[string]any{"uid": []string{"nameless"}, "sn": []string{"nameless"}, "randompassword": "R4nd0m"}}, nil
		}
		if c.Method == "group_add_member" {
			return map[string]any{"completed": 1, "failed": map[string]any{"member": map[string]any{"user": []any{}, "group": []any{}}}, "result": map[string]any{}}, nil
		}
		return groupOK(c)
	})
	cl, err := Connect(cfg, "google2ipa-managed")
	if err != nil {
		t.Fatal(err)
	}
	pw, err := cl.CreateUser(reconcile.User{UID: "nameless", GoogleUser: reconcile.GoogleUser{Email: "nameless@example.com"}})
	if err != nil || pw != "R4nd0m" {
		t.Fatalf("pw=%q err=%v", pw, err)
	}
	var add call
	for _, c := range calls() {
		if c.Method == "user_add" {
			add = c
		}
	}
	if add.Opts["givenname"] != "nameless" || add.Opts["sn"] != "nameless" || add.Opts["random"] != true {
		t.Errorf("user_add opts = %v", add.Opts)
	}
}

func TestAddToGroupsAlreadyMember(t *testing.T) {
	cfg, _ := fake(t, func(c call) (any, *freeipa.Error) {
		if c.Method == "group_add_member" {
			return map[string]any{"completed": 0, "result": map[string]any{}, "failed": map[string]any{"member": map[string]any{
				"user": []any{[]string{"john", "This entry is already a member"}}, "group": []any{}}}}, nil
		}
		return groupOK(c)
	})
	cl, err := Connect(cfg, "google2ipa-managed")
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.AddToGroups("john", []string{"staff"}); err != nil {
		t.Fatalf("already-a-member must not be an error: %v", err)
	}
}

func TestDisableEnableDelete(t *testing.T) {
	cfg, calls := fake(t, func(c call) (any, *freeipa.Error) {
		switch c.Method {
		case "user_mod":
			return map[string]any{"value": "john", "result": map[string]any{"uid": []string{"john"}, "sn": []string{"D"}}}, nil
		case "user_disable", "user_enable":
			return map[string]any{"value": "john", "result": true}, nil
		case "user_del":
			return map[string]any{"value": []string{"john"}, "result": map[string]any{"failed": []string{}}}, nil
		}
		return groupOK(c)
	})
	cl, err := Connect(cfg, "google2ipa-managed")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	if err := cl.Disable("john", at); err != nil {
		t.Fatal(err)
	}
	if err := cl.Enable("john"); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete("john", true); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, c := range calls() {
		seq = append(seq, c.Method)
		if c.Method == "user_mod" {
			seq = append(seq, c.Opts["setattr"].([]any)[0].(string))
		}
		if c.Method == "user_del" && c.Opts["preserve"] != true {
			t.Error("preserve not sent")
		}
	}
	want := "group_show user_mod krbprincipalexpiration=20260923100000Z user_disable user_enable user_mod krbprincipalexpiration= user_del"
	if strings.Join(seq, " ") != want {
		t.Errorf("calls:\n got %s\nwant %s", strings.Join(seq, " "), want)
	}
}
