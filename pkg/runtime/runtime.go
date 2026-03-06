package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type Runtime struct {
	binary string
	sudo   bool
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

func newRuntime(binary string) (*Runtime, error) {
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("%s not found in PATH", binary)
	}

	r := &Runtime{binary: binary}

	if runtime.GOOS != "darwin" {
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
	if runtime.GOOS == "linux" {
		return "localhost"
	}
	if r.binary == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
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

	// podman on linux may need sudo
	if r.binary == "podman" && runtime.GOOS == "linux" {
		r.sudo = info.rootless
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

// run executes the container runtime binary with the given args.
func (r *Runtime) run(args ...string) ([]byte, error) {
	name := r.binary
	cmdArgs := args
	if r.sudo {
		name = "sudo"
		cmdArgs = append([]string{r.binary}, args...)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, cmdArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", r.binary, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}
