package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	semver "k8s.io/apimachinery/pkg/util/version"
)

// ResolveContext supports stable @latest and opt-in @next channels, optionally
// scoped to a major or minor (4@latest, 5.0@next). Each resolves to an exact tag.
// Plain minors and exact tags remain offline catalogue selections.
func ResolveContext(ctx context.Context, selector string) (OCPVersion, error) {
	series, channel, remote := strings.Cut(selector, "@")
	if selector == "latest" || selector == "next" {
		series, channel, remote = "", selector, true
	}
	if !remote {
		return Resolve(selector)
	}
	if channel != "latest" && channel != "next" {
		return OCPVersion{}, fmt.Errorf("unknown release channel %q; use @latest (stable) or @next (including prereleases)", channel)
	}
	supported := series == ""
	for _, v := range catalogue {
		if matchesSeries(v, series) {
			supported = true
		}
	}
	if !supported {
		return OCPVersion{}, fmt.Errorf("unsupported release series %q; use a supported major or minor, e.g. 4@latest or 5.0@next", series)
	}
	versions, err := Published(ctx)
	if err != nil {
		return OCPVersion{}, err
	}
	return latestFor(versions, series, runtime.GOARCH, channel == "next")
}

func matchesSeries(v OCPVersion, series string) bool {
	return series == "" || v.ConsoleTag == series || strings.SplitN(v.ConsoleTag, ".", 2)[0] == series
}

func latestFor(versions []OCPVersion, series, arch string, prereleases bool) (OCPVersion, error) {
	for _, v := range versions {
		if matchesSeries(v, series) && slices.Contains(v.Arches, arch) && (prereleases || !v.IsPrerelease()) {
			return v, nil
		}
	}
	if series == "" {
		series = "supported versions"
	}
	if !prereleases {
		return OCPVersion{}, fmt.Errorf("no published stable OKD images for %s on %s; use @next to opt into prereleases", series, arch)
	}
	return OCPVersion{}, fmt.Errorf("no published OKD images for %s on %s; an oinc image must be built first", series, arch)
}

// Published lists oinc images for supported minors, newest first. Architectures
// come from published tags, rather than assuming both builds have completed.
func Published(ctx context.Context) ([]OCPVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return published(ctx, &http.Client{Timeout: 15 * time.Second}, "https://ghcr.io")
}

func published(ctx context.Context, client *http.Client, registry string) ([]OCPVersion, error) {
	const repository = "jasonmadigan/oinc"
	var auth struct {
		Token string `json:"token"`
	}
	if _, err := registryJSON(ctx, client, registry+"/token?service=ghcr.io&scope=repository:"+repository+":pull", "", &auth); err != nil {
		return nil, err
	}
	if auth.Token == "" {
		return nil, fmt.Errorf("registry returned an empty pull token")
	}
	var tags []string
	next := registry + "/v2/" + repository + "/tags/list?n=1000"
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return nil, fmt.Errorf("registry repeated a tags page")
		}
		seen[next] = true
		var page struct {
			Tags []string `json:"tags"`
		}
		pageURL := next
		link, err := registryJSON(ctx, client, pageURL, auth.Token, &page)
		if err != nil {
			return nil, err
		}
		tags = append(tags, page.Tags...)
		next = ""
		if link != "" {
			// GHCR uses the registry API's single Link: <url>; rel="next".
			start, end := strings.Index(link, "<"), strings.Index(link, ">")
			if start < 0 || end <= start {
				return nil, fmt.Errorf("invalid registry pagination link")
			}
			base, _ := url.Parse(pageURL)
			u, err := base.Parse(link[start+1 : end])
			if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.Path != "/v2/"+repository+"/tags/list" {
				return nil, fmt.Errorf("unexpected registry pagination URL")
			}
			next = u.String()
		}
	}
	return versionsFromTags(tags), nil
}

func registryJSON(ctx context.Context, client *http.Client, endpoint, token string, dest any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("listing published oinc images: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing published oinc images: registry returned %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dest); err != nil {
		return "", fmt.Errorf("reading registry response: %w", err)
	}
	return resp.Header.Get("Link"), nil
}

func versionsFromTags(tags []string) []OCPVersion {
	byTag := map[string]OCPVersion{}
	for _, tag := range tags {
		for _, arch := range []string{"amd64", "arm64"} {
			okd, ok := strings.CutSuffix(tag, "-"+arch)
			if !ok || !okdTagPattern.MatchString(okd) {
				continue
			}
			v, err := Resolve(okd)
			if err != nil || orderVersion(okd) == nil {
				continue
			}
			v.Arches = byTag[okd].Arches
			if !slices.Contains(v.Arches, arch) {
				v.Arches = append(v.Arches, arch)
				sort.Strings(v.Arches)
			}
			byTag[okd] = v
		}
	}
	versions := make([]OCPVersion, 0, len(byTag))
	for _, v := range byTag {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return orderVersion(versions[j].MicroShiftTag).LessThan(orderVersion(versions[i].MicroShiftTag))
	})
	return versions
}

// Treat numbered stable builds as later than ec/rc, retaining numeric ordering
// within each stage (rc.10 follows rc.9, and stable.16 follows stable.9).
func orderVersion(tag string) *semver.Version {
	core, suffix, _ := strings.Cut(tag, "-okd-scos.")
	if !strings.Contains(suffix, ".") {
		suffix = "stable." + suffix
	}
	v, _ := semver.ParseSemantic(core + "-" + suffix)
	return v
}
