package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The newest release fetched from GitHub, for Updates ▸ 1. Upload a release when "Fetch from online" is ticked (the same
// as in ddgw). It finds the tag the way a person would: the newest published release, otherwise the highest v<number> tag
// of the repository, downloads that tag's source archive and hands it to the same staging as an uploaded file, so it is
// checked exactly as an upload is and nothing is installed until "Update now". The repository is fixed (it is not a setting
// and nothing the signed-in user types reaches the request); the variables exist only so tests can point them at a fake.
var (
	ghRepo = "micush/dnsbench"
	ghAPI  = "https://api.github.com"
	ghHost = "https://github.com"
)

const fetchTimeout = 3 * time.Minute

var (
	ghTagRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	ghNumTagRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)*$`)
)

func ghGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dnsbench/"+version())
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: fetchTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("the download is larger than %d MB", limit>>20)
	}
	return b, nil
}

// numericTagLess orders v9 < v10 and 1.2 < 1.10.
func numericTagLess(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// candidateTags lists the tags to try, best first: the newest published release, then the repository's v<number> tags from
// the highest down (at most 10). More than one is returned because a repository's tags can include ones that are not
// dnsbench releases; the caller skips those.
func candidateTags(ctx context.Context) ([]string, error) {
	var out []string
	if b, err := ghGet(ctx, ghAPI+"/repos/"+ghRepo+"/releases/latest", 1<<20); err == nil {
		var r struct {
			Tag string `json:"tag_name"`
		}
		if json.Unmarshal(b, &r) == nil && r.Tag != "" && ghTagRe.MatchString(r.Tag) {
			out = append(out, r.Tag)
		}
	}
	b, err := ghGet(ctx, ghAPI+"/repos/"+ghRepo+"/tags?per_page=100", 4<<20)
	if err != nil {
		if len(out) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &tags); err != nil {
		if len(out) > 0 {
			return out, nil
		}
		return nil, errors.New("GitHub's answer was not understood")
	}
	var nums []string
	for _, t := range tags {
		if ghNumTagRe.MatchString(t.Name) && (len(out) == 0 || t.Name != out[0]) {
			nums = append(nums, t.Name)
		}
	}
	sort.Slice(nums, func(i, j int) bool { return numericTagLess(nums[j], nums[i]) })
	out = append(out, nums...)
	if len(out) > 10 {
		out = out[:10]
	}
	if len(out) == 0 {
		return nil, errors.New("no release or v<number> tag found in " + ghRepo + " (rate limit or no tags yet)")
	}
	return out, nil
}

// latestTag is the first candidate.
func latestTag(ctx context.Context) (string, error) {
	c, err := candidateTags(ctx)
	if err != nil {
		return "", err
	}
	return c[0], nil
}

// fetchTag downloads the source archive of one tag.
func fetchTag(ctx context.Context, tag string) ([]byte, error) {
	body, err := ghGet(ctx, ghHost+"/"+ghRepo+"/archive/refs/tags/"+tag+".tar.gz", maxUploadBytes)
	if err != nil {
		return nil, fmt.Errorf("download of %s failed: %w", tag, err)
	}
	return body, nil
}
