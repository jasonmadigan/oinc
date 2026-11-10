package addons

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jasonmadigan/oinc/pkg/download"
	utilversion "k8s.io/apimachinery/pkg/util/version"
)

const (
	rhclPackage       = "rhcl-operator"
	rhclStableChannel = "stable"

	rhclCatalogAPI = "https://catalog.redhat.com/api/containers/v1"
	// the API rejects larger pages
	rhclCatalogPageSize = 500
)

// rhclBundle is one row of the Red Hat catalog API's operator bundle list: a
// bundle version as carried by one channel of one index image.
type rhclBundle struct {
	Version         string `json:"version"`
	OCPVersion      string `json:"ocp_version"`
	Channel         string `json:"channel_name"`
	CSV             string `json:"csv_name"`
	LatestInChannel bool   `json:"latest_in_channel"`
	Index           string `json:"source_index_container_path"`
}

type rhclBundlePage struct {
	Data     []rhclBundle `json:"data"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int          `json:"total"`
}

// rhclRelease is an installable RHCL version and where OLM installs it from.
type rhclRelease struct {
	Version string
	CSV     string
	Channel string
	Index   string
}

type rhclCatalog []rhclBundle

// release resolves a version to the channel to subscribe to and the newest
// index image carrying it there. an empty version is the head of stable.
func (c rhclCatalog) release(version string) (rhclRelease, error) {
	if version == "" {
		return c.stableHead()
	}

	var carrying rhclCatalog
	for _, b := range c {
		if b.Version == version {
			carrying = append(carrying, b)
		}
	}
	if len(carrying) == 0 {
		return rhclRelease{}, fmt.Errorf("version %s is not in the Red Hat catalog; available: %s", version, strings.Join(c.versions(), ", "))
	}

	channel := carrying.preferredChannel(version)
	return carrying.newestIn(channel).release(), nil
}

// stableHead is what the stable channel installs from the newest index that
// has one. versions since 1.4.4 only ship on their minor channel.
func (c rhclCatalog) stableHead() (rhclRelease, error) {
	var heads rhclCatalog
	for _, b := range c {
		if b.Channel == rhclStableChannel && b.LatestInChannel {
			heads = append(heads, b)
		}
	}
	if len(heads) == 0 {
		hint := ""
		if versions := c.versions(); len(versions) > 0 {
			hint = fmt.Sprintf("; pin a version, e.g. rhcl@%s", versions[0])
		}
		return rhclRelease{}, fmt.Errorf("no index in the Red Hat catalog has a %s channel%s", rhclStableChannel, hint)
	}
	return heads.newestIn(rhclStableChannel).release(), nil
}

// preferredChannel picks the minor channel (stable-x.y) when the version is
// published there, then stable, then whatever carries it.
func (c rhclCatalog) preferredChannel(version string) string {
	channels := map[string]bool{}
	for _, b := range c {
		channels[b.Channel] = true
	}
	if i := strings.LastIndex(version, "."); i > 0 {
		if minor := rhclStableChannel + "-" + version[:i]; channels[minor] {
			return minor
		}
	}
	if channels[rhclStableChannel] {
		return rhclStableChannel
	}
	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0]
}

// newestIn returns the bundle from the newest index among those on channel.
// the caller guarantees at least one.
func (c rhclCatalog) newestIn(channel string) rhclBundle {
	var newest rhclBundle
	found := false
	for _, b := range c {
		if b.Channel != channel {
			continue
		}
		if !found || versionLess(newest.OCPVersion, b.OCPVersion) {
			newest, found = b, true
		}
	}
	return newest
}

func (b rhclBundle) release() rhclRelease {
	return rhclRelease{Version: b.Version, CSV: b.CSV, Channel: b.Channel, Index: b.Index}
}

// versions lists every distinct bundle version, newest first.
func (c rhclCatalog) versions() []string {
	seen := map[string]bool{}
	var versions []string
	for _, b := range c {
		if !seen[b.Version] {
			seen[b.Version] = true
			versions = append(versions, b.Version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	return versions
}

// releases resolves every version in the catalog, newest first.
func (c rhclCatalog) releases() []rhclRelease {
	var releases []rhclRelease
	for _, v := range c.versions() {
		if r, err := c.release(v); err == nil {
			releases = append(releases, r)
		}
	}
	return releases
}

// versionLess orders dotted numeric versions, so 4.9 sorts before 4.10.
// unparsable versions sort first.
func versionLess(a, b string) bool {
	va, errA := utilversion.ParseGeneric(a)
	vb, errB := utilversion.ParseGeneric(b)
	switch {
	case errA != nil && errB != nil:
		return a < b
	case errA != nil:
		return true
	case errB != nil:
		return false
	}
	return va.LessThan(vb)
}

// fetchRHCLCatalog lists every rhcl-operator bundle in the Red Hat operator
// indexes from the catalog API at baseURL.
func fetchRHCLCatalog(ctx context.Context, logger *slog.Logger, baseURL string) (rhclCatalog, error) {
	var catalog rhclCatalog
	for page := 0; ; page++ {
		query := url.Values{
			"filter":    {"package==" + rhclPackage + ";organization==redhat-operators"},
			"include":   {"data.version,data.ocp_version,data.channel_name,data.csv_name,data.latest_in_channel,data.source_index_container_path,total,page,page_size"},
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(rhclCatalogPageSize)},
		}
		data, err := download.URL(ctx, logger, baseURL+"/operators/bundles?"+query.Encode())
		if err != nil {
			return nil, err
		}
		var result rhclBundlePage
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("decoding Red Hat catalog response: %w", err)
		}
		catalog = append(catalog, result.Data...)
		if len(result.Data) == 0 || len(catalog) >= result.Total {
			break
		}
	}
	if len(catalog) == 0 {
		return nil, fmt.Errorf("the Red Hat catalog lists no %s bundles", rhclPackage)
	}
	return catalog, nil
}
