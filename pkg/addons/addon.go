package addons

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	// OpenShift minor the cluster corresponds to (4.23) and the openshift/api
	// branch that goes with it, for addons that install OpenShift API types.
	// empty when the cluster image is not a known version
	OpenShiftVersion   string
	OpenShiftAPIBranch string
	// LoadConsolePlugin has the cluster's Console load the plugin that a
	// ConsolePlugin resource describes. nil when there is no Console.
	LoadConsolePlugin func(ctx context.Context, name string) error
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

// ClusterValidator is implemented by addons that can only be installed on a
// cluster in a particular state. It runs once the cluster is reachable and
// before any addon in the request is installed.
type ClusterValidator interface {
	ValidateCluster(ctx context.Context, cfg *Config) error
}

// ValidateCluster runs the ClusterValidator hook on each addon.
func ValidateCluster(ctx context.Context, list []Addon, cfg *Config) error {
	for _, a := range list {
		if v, ok := a.(ClusterValidator); ok {
			if err := v.ValidateCluster(ctx, cfg); err != nil {
				return fmt.Errorf("%s: %w", a.Name(), err)
			}
		}
	}
	return nil
}

// PullSecretUser is implemented by addons whose images come from a registry
// that needs the stored pull secret inside the cluster.
type PullSecretUser interface {
	NeedsPullSecret() bool
}

// NeedPullSecret reports whether any addon needs the stored pull secret
// mounted into the cluster.
func NeedPullSecret(list []Addon) bool {
	for _, a := range list {
		if u, ok := a.(PullSecretUser); ok && u.NeedsPullSecret() {
			return true
		}
	}
	return false
}

// Noted is implemented by addons with a requirement worth showing wherever
// addons are listed.
type Noted interface {
	Note() string
}

// VersionChoice is a version an addon offers for an interactive install.
type VersionChoice struct {
	Version string
	Hint    string
	Default bool
}

// Versioned is implemented by addons whose installable versions come from a
// remote catalogue, so an interactive install can offer them.
type Versioned interface {
	Versions(ctx context.Context) ([]VersionChoice, error)
}

// Alternative is implemented by an addon that installs the same component as
// another one from a different source. The two cannot be combined, and option
// flags named for the other addon configure this one when it is requested.
type Alternative interface {
	AlternativeTo() string
}

var registry = map[string]Addon{}

// optionsSet tracks non-version options applied via Configure this
// invocation, so resolution can reject options whose addon is outside the
// requested set and installs can re-run a ready addon to apply them.
var optionsSet = map[string]map[string]string{}

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
				// an empty value unsets the option
				if opts[k] == "" {
					delete(optionsSet[name], k)
					continue
				}
				if optionsSet[name] == nil {
					optionsSet[name] = map[string]string{}
				}
				optionsSet[name][k] = opts[k]
			}
		}
	}
}

// alternativeConflict explains why an addon and its alternative cannot both
// be in the closure, naming whatever pulled the replaced addon in.
func alternativeConflict(name, replaced string, closure []string) error {
	msg := fmt.Sprintf("addons %s and %s cannot be installed together: %s replaces %s", name, replaced, name, replaced)
	var requirers []string
	for _, n := range closure {
		if slices.Contains(registry[n].Dependencies(), replaced) {
			requirers = append(requirers, n)
		}
	}
	if len(requirers) > 0 {
		msg += fmt.Sprintf(" (%s requires %s)", strings.Join(requirers, ", "), replaced)
	}
	return errors.New(msg)
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

	// an alternative replaces another addon: reject the pair, and hand the
	// alternative the options set through the replaced addon's flags
	closure := make([]string, 0, len(needed))
	for n := range needed {
		closure = append(closure, n)
	}
	sort.Strings(closure)
	for _, name := range closure {
		alt, ok := registry[name].(Alternative)
		if !ok {
			continue
		}
		replaced := alt.AlternativeTo()
		if needed[replaced] {
			return nil, alternativeConflict(name, replaced, closure)
		}
		if opts := optionsSet[replaced]; len(opts) > 0 {
			delete(optionsSet, replaced)
			Configure(name, opts)
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
