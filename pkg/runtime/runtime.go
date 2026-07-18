package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

type Runtime struct {
	binary string
}

// Detect finds an available container runtime. If override is non-empty, use that.
func Detect(override string) (*Runtime, error) {
	if override != "" {
		return newRuntime(override)
	}
	// docker first
	if _, err := exec.LookPath("docker"); err == nil {
		return newRuntime("docker")
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return newRuntime("podman")
	}
	return nil, fmt.Errorf("no container runtime found (need docker or podman)")
}

// DetectOwner finds the runtime that owns the named container. Unlike Detect,
// it probes every installed runtime, so on a host with both the pick matches
// wherever the container actually lives rather than the global probe order.
func DetectOwner(override, container string) (*Runtime, error) {
	if override != "" {
		r, err := newRuntime(override)
		if err != nil {
			return nil, err
		}
		if !r.ContainerExists(container) {
			return nil, fmt.Errorf("container %q not found in %s", container, override)
		}
		return r, nil
	}
	// no validate(): the container already exists under the picked runtime,
	// create-time checks do not apply
	var candidates []*Runtime
	for _, binary := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(binary); err == nil {
			candidates = append(candidates, &Runtime{binary: binary})
		}
	}
	// prefer a running container: a stale stopped one under another runtime
	// must not shadow the live cluster
	for _, r := range candidates {
		if r.ContainerRunning(container) {
			return r, nil
		}
	}
	for _, r := range candidates {
		if r.ContainerExists(container) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("container %q not found in docker or podman (is the cluster running?)", container)
}

func newRuntime(binary string) (*Runtime, error) {
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("%s not found in PATH", binary)
	}

	r := &Runtime{binary: binary}

	if goruntime.GOOS != "darwin" {
		if err := r.validate(); err != nil {
			return nil, err
		}
	}

	return r, nil
}

func (r *Runtime) Name() string { return r.binary }

// ContainerHostAddress returns the hostname the sidecar containers should use
// to reach services on the host.
func (r *Runtime) ContainerHostAddress() string {
	if goruntime.GOOS == "linux" && !RunningInWSL() {
		return "localhost"
	}
	if r.binary == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// UseHostNetwork returns whether sidecar containers should use the host network.
// Docker Desktop-backed WSL reports as Linux, but host networking does not publish
// ports back to Windows localhost there, so use normal port publishing instead.
func (r *Runtime) UseHostNetwork() bool {
	return goruntime.GOOS == "linux" && !RunningInWSL()
}

func RunningInWSL() bool {
	for _, path := range []string{"/proc/sys/kernel/osrelease", "/proc/version"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		contents := strings.ToLower(string(data))
		if strings.Contains(contents, "microsoft") || strings.Contains(contents, "wsl") {
			return true
		}
	}
	return false
}

func (r *Runtime) validate() error {
	info, err := r.runtimeInfo()
	if err != nil {
		return fmt.Errorf("failed to query %s info: %w", r.binary, err)
	}

	if !info.cgroupV2 {
		return fmt.Errorf("%s requires cgroup v2", r.binary)
	}
	if info.rootless {
		return fmt.Errorf("%s requires rootful mode", r.binary)
	}

	return nil
}

type runtimeInfo struct {
	cgroupV2 bool
	rootless bool
}

func (r *Runtime) runtimeInfo() (*runtimeInfo, error) {
	out, err := r.run("info", "--format", "json")
	if err != nil {
		return nil, err
	}

	// docker and podman return different JSON shapes, parse generically
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse %s info: %w", r.binary, err)
	}

	info := &runtimeInfo{}

	// cgroups: docker uses "CgroupVersion": "2", podman uses "cgroupVersion": "v2"
	if cg, ok := raw["CgroupVersion"]; ok {
		info.cgroupV2 = fmt.Sprint(cg) == "2"
	}
	if host, ok := raw["host"]; ok {
		if hostMap, ok := host.(map[string]any); ok {
			if cg, ok := hostMap["cgroupVersion"]; ok {
				info.cgroupV2 = fmt.Sprint(cg) == "v2"
			}
			if sec, ok := hostMap["security"]; ok {
				if secMap, ok := sec.(map[string]any); ok {
					if rl, ok := secMap["rootless"]; ok {
						info.rootless = fmt.Sprint(rl) == "true"
					}
				}
			}
		}
	}

	// docker rootless detection via SecurityOptions
	if secOpts, ok := raw["SecurityOptions"]; ok {
		if opts, ok := secOpts.([]any); ok {
			for _, opt := range opts {
				if fmt.Sprint(opt) == "name=rootless" {
					info.rootless = true
				}
			}
		}
	}

	return info, nil
}

// command builds an exec.Cmd for the runtime binary.
func (r *Runtime) command(args ...string) *exec.Cmd {
	return exec.Command(r.binary, args...)
}

// run executes the container runtime binary with the given args.
func (r *Runtime) run(args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := r.command(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", r.binary, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}
