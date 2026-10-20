package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNumericTagLess(t *testing.T) {
	if !numericTagLess("v215", "v1000") || numericTagLess("v1000", "v215") || !numericTagLess("1.2", "1.10") || numericTagLess("v5", "v5") {
		t.Fatal("tags are not ordered by number")
	}
}

// fakeGitHub serves the two places fetchRelease asks: the API (release/tag lists) and the archive.
func fakeGitHub(t *testing.T, release string, tags string, archives map[string][]byte) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		switch {
		case r.URL.Path == "/repos/micush/dnsdir/releases/latest":
			if release == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(`{"tag_name":"` + release + `"}`))
		case r.URL.Path == "/repos/micush/dnsdir/tags":
			w.Write([]byte(tags))
		case strings.HasPrefix(r.URL.Path, "/micush/dnsdir/archive/refs/tags/"):
			tag := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/micush/dnsdir/archive/refs/tags/"), ".tar.gz")
			if b, ok := archives[tag]; ok {
				w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	oa, oh := ghAPI, ghHost
	ghAPI, ghHost = srv.URL, srv.URL
	t.Cleanup(func() { srv.Close(); ghAPI, ghHost = oa, oh })
	return &n
}

func TestLatestTag(t *testing.T) {
	fakeGitHub(t, "v300", `[]`, nil)
	if tag, err := latestTag(context.Background(), ghRepo); err != nil || tag != "v300" {
		t.Fatalf("release: %q %v", tag, err)
	}
	fakeGitHub(t, "", `[{"name":"v99"},{"name":"v1000"},{"name":"nightly"},{"name":"v215"}]`, nil)
	if tag, err := latestTag(context.Background(), ghRepo); err != nil || tag != "v1000" {
		t.Fatalf("tags: %q %v", tag, err)
	}
	fakeGitHub(t, "", `[{"name":"nightly"}]`, nil)
	if _, err := latestTag(context.Background(), ghRepo); err == nil {
		t.Fatal("no usable tag was accepted")
	}
}

func TestUpdateFetchFromGitHub(t *testing.T) {
	e := newWebEnv(t)
	e.login("alice", "pw")
	fakeGitHub(t, "v7", `[]`, map[string][]byte{"v7": fakeSourceTarball(t, "7")})
	r := e.do("POST", "/api/update/fetch", map[string]any{}, withAuth(e, true))
	if r.code != 200 {
		t.Fatalf("fetch: %d %s", r.code, r.raw)
	}
	if d := r.body["data"].(map[string]any); d["version"] != "7" || d["tag"] != "v7" {
		t.Fatalf("fetch answer: %s", r.raw)
	}
	st := e.do("GET", "/api/update", nil, withAuth(e, false))
	if st.body["data"].(map[string]any)["source_version"] != "7" {
		t.Fatalf("not staged: %s", st.raw)
	}
	// GitHub unreachable or the tag missing: a clear error, nothing staged over the good one
	fakeGitHub(t, "v8", `[]`, nil)
	if r := e.do("POST", "/api/update/fetch", map[string]any{}, withAuth(e, true)); r.code != 422 || !strings.Contains(string(r.raw), "v8") {
		t.Fatalf("missing archive: %d %s", r.code, r.raw)
	}
	fakeGitHub(t, "v9", `[]`, map[string][]byte{"v9": []byte("not an archive")})
	if r := e.do("POST", "/api/update/fetch", map[string]any{}, withAuth(e, true)); r.code != 422 {
		t.Fatalf("junk archive: %d %s", r.code, r.raw)
	}
}

func TestNormalizeRepo(t *testing.T) {
	good := map[string]string{
		"":                                       "",
		"default":                                "",
		"https://github.com/micush/dnsdir":       "", // the default is stored as empty
		"https://github.com/micush/dnsdir/":      "",
		"github.com/micush/dnsdir.git":           "",
		"micush/dnsdir":                          "",
		"https://github.com/someone/dnsdir-fork": "someone/dnsdir-fork",
		"  https://www.github.com/a.b/c_d.git ":  "a.b/c_d",
		"someone/fork":                           "someone/fork",
	}
	for in, want := range good {
		got, err := normalizeRepo(in)
		if err != nil || got != want {
			t.Errorf("normalizeRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"https://gitlab.com/a/b", "https://example.com/a/b", "a", "a/b/c", "https://github.com/a", "https://github.com/a/b/c",
		"https://github.com/../b", "a/..", "a b/c", "https://github.com/a/b?x=1", "-a/b", "git@github.com:a/b",
	} {
		if got, err := normalizeRepo(in); err == nil {
			t.Errorf("normalizeRepo(%q) = %q, want an error", in, got)
		}
	}
}

// The repository is part of the shared update settings: set on the primary, kept in the intent, used by the fetch.
func TestUpdateRepoSetting(t *testing.T) {
	u, err := NewUpdater(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if u.Repo() != ghRepo {
		t.Fatalf("default repo = %q", u.Repo())
	}
	u.SetRepo("someone/fork", "tester", "n1")
	if u.Repo() != "someone/fork" || u.Intent().Repo != "someone/fork" {
		t.Fatalf("repo not set: %q", u.Repo())
	}
	u.SetRepo("", "tester", "n1")
	if u.Repo() != ghRepo {
		t.Fatalf("empty must go back to the default: %q", u.Repo())
	}
}
