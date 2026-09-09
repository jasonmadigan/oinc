package version

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPublished(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:jasonmadigan/oinc:pull" {
				t.Error("missing repository pull scope")
			}
			fmt.Fprint(w, `{"token":"test-token"}`)
		case "/v2/jasonmadigan/oinc/tags/list":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("missing pull token")
			}
			if r.URL.Query().Get("last") == "page1" {
				fmt.Fprint(w, `{"tags":["5.0.0-okd-scos.rc.10-arm64","5.0.0-okd-scos.rc.10-amd64"]}`)
				return
			}
			w.Header().Set("Link", `<?last=page1>; rel="next"`)
			fmt.Fprint(w, `{"tags":["5.0.0-okd-scos.rc.9-arm64","5.0.0-okd-scos.ec.8-amd64","unrelated","6.0.0-okd-scos.rc.1-arm64"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	got, err := published(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, v := range got {
		tags = append(tags, v.Version)
		if v.ConsoleTag != "5.0" || v.APIBranch != "release-5.0" {
			t.Errorf("wrong compatibility settings: %+v", v)
		}
	}
	want := []string{"5.0.0-okd-scos.rc.10", "5.0.0-okd-scos.rc.9", "5.0.0-okd-scos.ec.8"}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	if !reflect.DeepEqual(got[0].Arches, []string{"amd64", "arm64"}) {
		t.Fatalf("arches = %v", got[0].Arches)
	}
}

func TestPublishedErrors(t *testing.T) {
	for _, mode := range []string{"unauthorized", "malformed", "empty token", "external link", "repeated page", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "unauthorized" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if mode == "malformed" {
					fmt.Fprint(w, "not JSON")
					return
				}
				if mode == "empty token" {
					fmt.Fprint(w, `{}`)
					return
				}
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"token":"test-token"}`)
					return
				}
				if mode == "external link" {
					w.Header().Set("Link", `<https://example.invalid/tags>; rel="next"`)
				} else {
					w.Header().Set("Link", `<?n=1000>; rel="next"`)
				}
				fmt.Fprint(w, `{"tags":[]}`)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if _, err := published(ctx, server.Client(), server.URL); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPublishedOrderingAndLatestArchitecture(t *testing.T) {
	versions := versionsFromTags([]string{
		"5.0.0-okd-scos.ec.99-arm64", "5.0.0-okd-scos.rc.9-arm64",
		"5.0.0-okd-scos.rc.10-arm64", "5.0.0-okd-scos.9-arm64",
		"5.0.0-okd-scos.16-arm64", "5.0.1-okd-scos.ec.1-amd64",
		"5.0.0-okd-scos.rc.10-arm64", // duplicate
		"5.0.0-okd-scos.rc.01-arm64", // non-semantic tag
	})
	want := []string{"5.0.1-okd-scos.ec.1", "5.0.0-okd-scos.16", "5.0.0-okd-scos.9", "5.0.0-okd-scos.rc.10", "5.0.0-okd-scos.rc.9", "5.0.0-okd-scos.ec.99"}
	var got []string
	for _, v := range versions {
		got = append(got, v.Version)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	latest, err := latestFor(versions, "5.0", "arm64", true)
	if err != nil || latest.Version != "5.0.0-okd-scos.16" {
		t.Fatalf("latest for ARM = %+v, %v", latest, err)
	}
	if _, err := latestFor(nil, "5.0", "arm64", true); err == nil || !strings.Contains(err.Error(), "no published OKD images") {
		t.Fatalf("missing builds: %v", err)
	}
}

func TestResolveContext(t *testing.T) {
	for _, selector := range []string{"5.0@invalid", "6.0@latest", "5.0.0-okd-scos.rc.1@latest"} {
		if _, err := ResolveContext(context.Background(), selector); err == nil {
			t.Errorf("accepted invalid latest selector %q", selector)
		}
	}
	// A cancelled context proves exact tags and catalogue pins stay offline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, selector := range []string{"5.0", "5.0.0-okd-scos.rc.1"} {
		if _, err := ResolveContext(ctx, selector); err != nil {
			t.Errorf("offline resolution of %q: %v", selector, err)
		}
	}
}

func TestStableChannels(t *testing.T) {
	versions := versionsFromTags([]string{
		"5.0.0-okd-scos.rc.1-arm64",
		"4.22.0-okd-scos.ec.16-arm64",
		"4.21.0-okd-scos.1-arm64",
		"4.20.0-okd-scos.16-arm64",
	})
	for _, tt := range []struct {
		series      string
		prereleases bool
		want        string
	}{
		{"", false, "4.21.0-okd-scos.1"},
		{"4", false, "4.21.0-okd-scos.1"},
		{"4.20", false, "4.20.0-okd-scos.16"},
		{"5", false, ""},
		{"5.0", false, ""},
		{"5.0", true, "5.0.0-okd-scos.rc.1"},
		{"", true, "5.0.0-okd-scos.rc.1"},
	} {
		got, err := latestFor(versions, tt.series, "arm64", tt.prereleases)
		if tt.want == "" {
			if err == nil {
				t.Errorf("%s stable channel accepted prerelease: %+v", tt.series, got)
			}
		} else if err != nil || got.Version != tt.want {
			t.Errorf("series %q prereleases=%v: %+v, %v, want %s", tt.series, tt.prereleases, got, err, tt.want)
		}
	}
	if Default().Version != "4.20" || Default().IsPrerelease() {
		t.Fatalf("unstable default: %+v", Default())
	}
}
