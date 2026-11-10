package addons

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// fixtureRHCLCatalog loads the catalog API response captured on 2026-11-10.
func fixtureRHCLCatalog(t *testing.T) rhclCatalog {
	t.Helper()
	data, err := os.ReadFile("testdata/rhcl-bundles.json")
	if err != nil {
		t.Fatal(err)
	}
	var page rhclBundlePage
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 85 {
		t.Fatalf("fixture has %d bundles, want 85", len(page.Data))
	}
	return rhclCatalog(page.Data)
}

func TestRHCLCatalogRelease(t *testing.T) {
	catalog := fixtureRHCLCatalog(t)

	tests := []struct {
		name    string
		version string
		want    rhclRelease
	}{
		{
			name:    "pinned version uses its minor channel and the newest index carrying it",
			version: "1.4.3",
			want: rhclRelease{
				Version: "1.4.3",
				CSV:     "rhcl-operator.v1.4.3",
				Channel: "stable-1.4",
				Index:   "registry.redhat.io/redhat/redhat-operator-index:v4.22",
			},
		},
		{
			name:    "version published before minor channels falls back to stable",
			version: "1.4.2",
			want: rhclRelease{
				Version: "1.4.2",
				CSV:     "rhcl-operator.v1.4.2",
				Channel: "stable",
				Index:   "registry.redhat.io/redhat/redhat-operator-index:v4.22",
			},
		},
		{
			name:    "index newer than 4.x wins",
			version: "1.5.0",
			want: rhclRelease{
				Version: "1.5.0",
				CSV:     "rhcl-operator.v1.5.0",
				Channel: "stable-1.5",
				Index:   "registry.redhat.io/redhat/redhat-operator-index:v5.0",
			},
		},
		{
			name:    "version dropped from newer indexes resolves to the last one carrying it",
			version: "1.2.1",
			want: rhclRelease{
				Version: "1.2.1",
				CSV:     "rhcl-operator.v1.2.1",
				Channel: "stable",
				Index:   "registry.redhat.io/redhat/redhat-operator-index:v4.20",
			},
		},
		{
			name:    "no version installs the head of stable",
			version: "",
			want: rhclRelease{
				Version: "1.4.3",
				CSV:     "rhcl-operator.v1.4.3",
				Channel: "stable",
				Index:   "registry.redhat.io/redhat/redhat-operator-index:v4.22",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := catalog.release(tt.version)
			if err != nil {
				t.Fatalf("release(%q): %v", tt.version, err)
			}
			if got != tt.want {
				t.Errorf("release(%q) = %+v, want %+v", tt.version, got, tt.want)
			}
		})
	}
}

func TestRHCLCatalogReleaseUnknownVersion(t *testing.T) {
	catalog := fixtureRHCLCatalog(t)

	_, err := catalog.release("9.9.9")
	if err == nil {
		t.Fatal("want an error for a version the catalog does not have")
	}
	for _, want := range []string{"9.9.9", "1.5.0", "1.4.4", "1.4.3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestRHCLCatalogReleaseComparesIndexVersionsNumerically(t *testing.T) {
	catalog := rhclCatalog{
		{Version: "2.0.0", OCPVersion: "4.9", Channel: "stable", CSV: "rhcl-operator.v2.0.0", Index: "example.test/index:v4.9"},
		{Version: "2.0.0", OCPVersion: "4.10", Channel: "stable", CSV: "rhcl-operator.v2.0.0", Index: "example.test/index:v4.10"},
	}

	got, err := catalog.release("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Index != "example.test/index:v4.10" {
		t.Errorf("index = %q, want the 4.10 index", got.Index)
	}
}

func TestRHCLCatalogReleaseWithoutStableChannel(t *testing.T) {
	catalog := rhclCatalog{
		{Version: "2.0.0", OCPVersion: "5.0", Channel: "stable-2.0", CSV: "rhcl-operator.v2.0.0", Index: "example.test/index:v5.0", LatestInChannel: true},
	}

	_, err := catalog.release("")
	if err == nil {
		t.Fatal("want an error when no index has a stable channel")
	}
	if !strings.Contains(err.Error(), "rhcl@2.0.0") {
		t.Errorf("error %q does not suggest pinning an available version", err)
	}
}

func TestRHCLCatalogReleases(t *testing.T) {
	catalog := fixtureRHCLCatalog(t)

	releases := catalog.releases()

	var versions []string
	for _, r := range releases {
		versions = append(versions, r.Version)
	}
	want := "1.5.0 1.4.4 1.4.3 1.4.2 1.4.1 1.4.0 1.3.7 1.3.6 1.3.5 1.3.4 1.3.3 1.3.2 1.3.1 1.3.0 1.2.1 1.2.0 1.1.1 1.1.0 1.0.2 1.0.1 1.0.0"
	if got := strings.Join(versions, " "); got != want {
		t.Fatalf("versions = %s\nwant       %s", got, want)
	}

	// each entry is what pinning that version installs
	if got, want := releases[1], (rhclRelease{
		Version: "1.4.4",
		CSV:     "rhcl-operator.v1.4.4",
		Channel: "stable-1.4",
		Index:   "registry.redhat.io/redhat/redhat-operator-index:v4.22",
	}); got != want {
		t.Errorf("releases[1] = %+v, want %+v", got, want)
	}
}

// catalogServer serves bundle rows two per page, recording each request's query.
func catalogServer(t *testing.T, rows []rhclBundle) (*httptest.Server, *[]url.Values) {
	t.Helper()
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start := min(page*2, len(rows))
		end := min(start+2, len(rows))
		if err := json.NewEncoder(w).Encode(rhclBundlePage{Data: rows[start:end], Page: page, PageSize: 2, Total: len(rows)}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

func TestFetchRHCLCatalogReadsEveryPage(t *testing.T) {
	rows := []rhclBundle{
		{Version: "1.4.2", OCPVersion: "4.22", Channel: "stable", CSV: "rhcl-operator.v1.4.2", Index: "example.test/index:v4.22"},
		{Version: "1.4.3", OCPVersion: "4.22", Channel: "stable", CSV: "rhcl-operator.v1.4.3", Index: "example.test/index:v4.22", LatestInChannel: true},
		{Version: "1.5.0", OCPVersion: "5.0", Channel: "stable-1.5", CSV: "rhcl-operator.v1.5.0", Index: "example.test/index:v5.0", LatestInChannel: true},
	}
	srv, queries := catalogServer(t, rows)

	catalog, err := fetchRHCLCatalog(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual([]rhclBundle(catalog), rows) {
		t.Errorf("catalog = %+v\nwant      %+v", catalog, rows)
	}
	if len(*queries) != 2 {
		t.Fatalf("made %d requests, want one per page (2)", len(*queries))
	}
	for i, q := range *queries {
		if got, want := q.Get("filter"), "package==rhcl-operator;organization==redhat-operators"; got != want {
			t.Errorf("request %d filter = %q, want %q", i, got, want)
		}
		if got, want := q.Get("page"), strconv.Itoa(i); got != want {
			t.Errorf("request %d page = %q, want %q", i, got, want)
		}
	}
}

func TestFetchRHCLCatalogFailsOnAnEmptyCatalog(t *testing.T) {
	srv, _ := catalogServer(t, nil)

	_, err := fetchRHCLCatalog(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)
	if err == nil {
		t.Fatal("want an error when the catalog lists no rhcl-operator bundles")
	}
}

func TestFetchRHCLCatalogReportsServerErrors(t *testing.T) {
	noDownloadDelays(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchRHCLCatalog(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)
	if err == nil {
		t.Fatal("want an error when the catalog API fails")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not carry the HTTP status", err)
	}
}
