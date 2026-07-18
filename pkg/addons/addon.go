package addons

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jasonmadigan/oinc/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Kubeconfig    []byte
	DynamicClient dynamic.Interface
	Clientset     kubernetes.Interface
	Runtime       *runtime.Runtime
	Logger        *slog.Logger
	// cluster container name, for addons that inspect the container's network
	Container string
	// cluster ingress hostname and the host port mapped to container port 80,
	// for addons that expose Routes on the ports oinc already maps.
	// IngressErr carries the cause when the port could not be determined.
	IngressHost     string
	IngressHTTPPort int
	IngressErr      error
}

type Addon interface {
	Name() string
	Dependencies() []string
	Install(ctx context.Context, cfg *Config) error
	Ready(ctx context.Context, cfg *Config) error
}

// Configurable is implemented by addons that accept options (e.g. version).
type Configurable interface {
	SetOptions(opts map[string]string)
}

// Validator is implemented by addons that can check their configuration
// before any cluster work starts.
type Validator interface {
	Validate() error
}

// Validate runs the Validator hook on each addon, including dependency-pulled
// ones, so bad config fails before any cluster work.
func Validate(list []Addon) error {
	for _, a := range list {
		if v, ok := a.(Validator); ok {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("%s: %w", a.Name(), err)
			}
		}
	}
	return nil
}

var registry = map[string]Addon{}

// optionsSet tracks non-version options applied via Configure this
// invocation, so resolution can reject options whose addon is outside the
// requested set and installs can re-run a ready addon to apply them.
var optionsSet = map[string]map[string]bool{}

// HasOptions reports whether non-version options were applied to an addon.
func HasOptions(name string) bool { return len(optionsSet[name]) > 0 }

// AnyOptions reports whether any addon had non-version options applied.
func AnyOptions() bool {
	for _, keys := range optionsSet {
		if len(keys) > 0 {
			return true
		}
	}
	return false
}

func Register(a Addon) { registry[a.Name()] = a }

func Get(name string) (Addon, bool) {
	a, ok := registry[name]
	return a, ok
}

func All() map[string]Addon { return registry }

// Configure applies options to a registered addon ahead of Resolve.
// No-op for unknown or non-configurable addons.
func Configure(name string, opts map[string]string) {
	if len(opts) == 0 {
		return
	}
	if a, ok := registry[name]; ok {
		if c, ok := a.(Configurable); ok {
			c.SetOptions(opts)
			for k := range opts {
				if k == "version" {
					continue
				}
				if optionsSet[name] == nil {
					optionsSet[name] = map[string]bool{}
				}
				optionsSet[name][k] = true
			}
		}
	}
}

// ParseAddonSpec splits "name@version" into name + options.
// e.g. "cert-manager@1.16.0" -> "cert-manager", {"version":"1.16.0"}
func ParseAddonSpec(spec string) (string, map[string]string) {
	opts := map[string]string{}
	if i := strings.Index(spec, "@"); i >= 0 {
		opts["version"] = spec[i+1:]
		return spec[:i], opts
	}
	return spec, opts
}

// Resolve returns addons in dependency order. Errors on unknown names or cycles.
// Accepts specs like "cert-manager@1.16.0" and configures addons accordingly.
func Resolve(specs []string) ([]Addon, error) {
	// parse specs and apply options
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		name, opts := ParseAddonSpec(spec)
		if name == "" {
			return nil, fmt.Errorf("invalid addon spec %q: empty addon name", spec)
		}
		if v, ok := opts["version"]; ok && v == "" {
			return nil, fmt.Errorf("invalid addon spec %q: empty version after @", spec)
		}
		names = append(names, name)
		Configure(name, opts)
	}

	for _, n := range names {
		if _, ok := registry[n]; !ok {
			var avail []string
			for k := range registry {
				avail = append(avail, k)
			}
			sort.Strings(avail)
			return nil, fmt.Errorf("unknown addon %q, available: %v", n, avail)
		}
	}

	// collect all required addons including transitive deps
	needed := map[string]bool{}
	var collect func(string) error
	collect = func(name string) error {
		if needed[name] {
			return nil
		}
		a, ok := registry[name]
		if !ok {
			return fmt.Errorf("addon %q required as dependency but not registered", name)
		}
		needed[name] = true
		for _, dep := range a.Dependencies() {
			if err := collect(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range names {
		if err := collect(n); err != nil {
			return nil, err
		}
	}

	// options for addons outside the closure would be silently ignored;
	// fail naming the flag spelling (--<addon>-<option>) instead
	var orphaned []string
	for name := range optionsSet {
		if HasOptions(name) && !needed[name] {
			orphaned = append(orphaned, name)
		}
	}
	if len(orphaned) > 0 {
		sort.Strings(orphaned)
		name := orphaned[0]
		var flags []string
		for k := range optionsSet[name] {
			flags = append(flags, "--"+name+"-"+k)
		}
		sort.Strings(flags)
		return nil, fmt.Errorf("%s set but addon %q is not in the requested set; add it to the addon list", strings.Join(flags, ", "), name)
	}

	// topological sort (kahn's algorithm)
	inDegree := map[string]int{}
	for n := range needed {
		if _, ok := inDegree[n]; !ok {
			inDegree[n] = 0
		}
		for _, dep := range registry[n].Dependencies() {
			inDegree[dep] = inDegree[dep] // ensure entry exists
		}
	}
	for n := range needed {
		for _, dep := range registry[n].Dependencies() {
			_ = dep
			inDegree[n]++
		}
	}

	var queue []string
	for n, d := range inDegree {
		if d == 0 && needed[n] {
			queue = append(queue, n)
		}
	}

	var sorted []Addon
	visited := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		sorted = append(sorted, registry[name])

		for n := range needed {
			if visited[n] {
				continue
			}
			for _, dep := range registry[n].Dependencies() {
				if dep == name {
					inDegree[n]--
				}
			}
			if inDegree[n] == 0 {
				queue = append(queue, n)
			}
		}
	}

	if len(sorted) != len(needed) {
		return nil, fmt.Errorf("dependency cycle detected")
	}

	return sorted, nil
}
