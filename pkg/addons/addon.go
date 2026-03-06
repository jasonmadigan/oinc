package addons

import (
	"context"
	"fmt"
	"log/slog"

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
}

type Addon interface {
	Name() string
	Dependencies() []string
	Install(ctx context.Context, cfg *Config) error
	Ready(ctx context.Context, cfg *Config) error
}

var registry = map[string]Addon{}

func Register(a Addon) { registry[a.Name()] = a }

func Get(name string) (Addon, bool) {
	a, ok := registry[name]
	return a, ok
}

func All() map[string]Addon { return registry }

// Resolve returns addons in dependency order. Errors on unknown names or cycles.
func Resolve(names []string) ([]Addon, error) {
	for _, n := range names {
		if _, ok := registry[n]; !ok {
			var avail []string
			for k := range registry {
				avail = append(avail, k)
			}
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
