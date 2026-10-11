package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves the API and the archive host; latest == "" means no published release (so the tags list is used).
func fakeGitHub(t *testing.T, latest string, tags []string, archives map[string][]byte) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+ghRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if latest == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"` + latest + `"}`))
	})
	mux.HandleFunc("/repos/"+ghRepo+"/tags", func(w http.ResponseWriter, r *http.Request) {
		var parts []string
		for _, n := range tags {
			parts = append(parts, `{"name":"`+n+`"}`)
		}
		w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
	})
	mux.HandleFunc("/"+ghRepo+"/archive/refs/tags/", func(w http.ResponseWriter, r *http.Request) {
		tag := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"+ghRepo+"/archive/refs/tags/"), ".tar.gz")
		b, ok := archives[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})
	srv := httptest.NewServer(mux)
	oa, oh := ghAPI, ghHost
	ghAPI, ghHost = srv.URL, srv.URL
	t.Cleanup(func() { ghAPI, ghHost = oa, oh; srv.Close() })
}

// githubTgz wraps a release the way GitHub does: everything under one dnsbench-<tag>/ directory.
func githubTgz(t *testing.T, ver string) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(releaseTgz(t, ver)))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "dnsbench")
		h.Name = "dnsbench-v" + ver + rest
		var data bytes.Buffer
		data.ReadFrom(tr)
		h.Size = int64(data.Len())
		tw.WriteHeader(h)
		tw.Write(data.Bytes())
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestNumericTagLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"v9", "v10", true}, {"v10", "v9", false}, {"v12", "v12", false}, {"1.2", "1.10", true}, {"v1", "v1.1", true}} {
		if numericTagLess(c.a, c.b) != c.less {
			t.Errorf("%s < %s should be %v", c.a, c.b, c.less)
		}
	}
}

func TestLatestTagPrefersReleaseThenHighestTag(t *testing.T) {
	fakeGitHub(t, "v40", []string{"v9", "v10"}, nil)
	if tag, err := latestTag(t.Context()); err != nil || tag != "v40" {
		t.Errorf("release: %q %v", tag, err)
	}
	fakeGitHub(t, "", []string{"v9", "junk", "v10", "v2"}, nil)
	if tag, err := latestTag(t.Context()); err != nil || tag != "v10" {
		t.Errorf("tags: %q %v", tag, err)
	}
	fakeGitHub(t, "", []string{"junk"}, nil)
	if _, err := latestTag(t.Context()); err == nil {
		t.Error("no usable tag must be an error")
	}
}

func TestFetchStagesLikeAnUploadAndRecordsIt(t *testing.T) {
	us := newUpdateServer(t)
	ver := nextVersion(t)
	fakeGitHub(t, "v"+ver, nil, map[string][]byte{"v" + ver: githubTgz(t, ver)})
	resp, body := us.do(t, "POST", "/api/update/fetch", "", us.hdr)
	if resp.StatusCode != 200 || !strings.Contains(body, `"version":"`+ver+`"`) || !strings.Contains(body, `"tag":"v`+ver+`"`) {
		t.Fatalf("fetch: %d %s", resp.StatusCode, body)
	}
	if us.app.updater.SourceVersion() != ver {
		t.Errorf("staged %q, want %q", us.app.updater.SourceVersion(), ver)
	}
	if us.restarts.Load() != 0 || readStr(t, us.exe) != "OLD" {
		t.Error("fetching must not install anything")
	}
}

func TestFetchFailuresAreReportedAndStageNothing(t *testing.T) {
	us := newUpdateServer(t)
	ver := nextVersion(t)
	// release names a tag whose archive is missing
	fakeGitHub(t, "v"+ver, nil, nil)
	if resp, body := us.do(t, "POST", "/api/update/fetch", "", us.hdr); resp.StatusCode != 422 || !strings.Contains(body, "download") {
		t.Errorf("missing archive: %d %s", resp.StatusCode, body)
	}
	// junk archive
	fakeGitHub(t, "v"+ver, nil, map[string][]byte{"v" + ver: []byte("not a tarball")})
	if resp, _ := us.do(t, "POST", "/api/update/fetch", "", us.hdr); resp.StatusCode != 422 {
		t.Errorf("junk archive: %d", resp.StatusCode)
	}
	// not newer than the running version
	old := version()
	fakeGitHub(t, "v"+old, nil, map[string][]byte{"v" + old: githubTgz(t, old)})
	if resp, _ := us.do(t, "POST", "/api/update/fetch", "", us.hdr); resp.StatusCode != 422 {
		t.Errorf("not newer: %d", resp.StatusCode)
	}
	if us.app.updater.SourceVersion() != "" {
		t.Error("a failed fetch staged something")
	}
}
