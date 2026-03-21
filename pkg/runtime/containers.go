package runtime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ContainerOpts struct {
	Name       string
	Image      string
	Hostname   string
	Labels     map[string]string
	Ports      []PortMapping
	Volumes    []string
	Privileged bool
	Platform   string
	Env        map[string]string
	Network    string // e.g. "host" for --network=host
}

type PortMapping struct {
	Host      int
	Container int
	BindIP    string // empty = 127.0.0.1
}

func (r *Runtime) PullImage(image string, platform string) error {
	// check if already present
	if _, err := r.run("image", "inspect", image); err == nil {
		return nil
	}
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, image)
	_, err := r.run(args...)
	return err
}

func (r *Runtime) CreateContainer(opts ContainerOpts) error {
	args := []string{"create"}

	if opts.Hostname != "" {
		args = append(args, "--hostname", opts.Hostname)
	}

	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	if opts.Privileged {
		args = append(args, "--privileged")
	}

	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}

	for _, p := range opts.Ports {
		bind := p.BindIP
		if bind == "" {
			bind = "127.0.0.1"
		}
		// ports below 1024 on macOS can't bind to 127.0.0.1
		if p.Host < 1024 {
			args = append(args, "-p", fmt.Sprintf("%d:%d", p.Host, p.Container))
		} else {
			args = append(args, "-p", fmt.Sprintf("%s:%d:%d", bind, p.Host, p.Container))
		}
	}

	for _, v := range opts.Volumes {
		args = append(args, "-v", v)
	}

	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	}

	args = append(args, "--name", opts.Name, opts.Image)

	_, err := r.run(args...)
	return err
}

func (r *Runtime) StartContainer(name string) error {
	_, err := r.run("start", name)
	return err
}

func (r *Runtime) StopContainer(name string) error {
	_, err := r.run("stop", name)
	return err
}

func (r *Runtime) RemoveContainer(name string) error {
	_, err := r.run("rm", "-f", name)
	return err
}

func (r *Runtime) ContainerExists(name string) bool {
	_, err := r.run("inspect", name)
	return err == nil
}

func (r *Runtime) ContainerRunning(name string) bool {
	out, err := r.run("inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func (r *Runtime) ExecInContainer(container string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"exec", container}, args...)
	return r.run(cmdArgs...)
}

func (r *Runtime) WaitForService(container, service string, retries int, delay time.Duration) error {
	for i := 0; i < retries; i++ {
		out, err := r.ExecInContainer(container, "systemctl", "is-active", service)
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return nil
		}
		time.Sleep(delay)
	}
	return fmt.Errorf("service %s not active after %d attempts", service, retries)
}

func (r *Runtime) CopyFromContainer(container, path string) ([]byte, error) {
	return r.ExecInContainer(container, "cat", path)
}

type ContainerInfo struct {
	Running   bool
	Image     string
	StartedAt time.Time
	Ports     map[int]int // container port -> host port
}

func (r *Runtime) InspectContainer(name string) (*ContainerInfo, error) {
	out, err := r.run("inspect", name)
	if err != nil {
		return nil, err
	}

	var raw []map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing inspect output: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty inspect result for %s", name)
	}

	data := raw[0]
	info := &ContainerInfo{Ports: map[int]int{}}

	if state, ok := data["State"].(map[string]any); ok {
		info.Running = fmt.Sprint(state["Running"]) == "true"
		if sa, ok := state["StartedAt"].(string); ok {
			info.StartedAt, _ = time.Parse(time.RFC3339Nano, sa)
		}
	}

	if cfg, ok := data["Config"].(map[string]any); ok {
		if img, ok := cfg["Image"].(string); ok {
			info.Image = img
		}
	}

	if ns, ok := data["NetworkSettings"].(map[string]any); ok {
		if ports, ok := ns["Ports"].(map[string]any); ok {
			for cp, bindings := range ports {
				parts := strings.SplitN(cp, "/", 2)
				if len(parts) == 0 {
					continue
				}
				containerPort, err := strconv.Atoi(parts[0])
				if err != nil {
					continue
				}
				arr, ok := bindings.([]any)
				if !ok || len(arr) == 0 {
					continue
				}
				if bind, ok := arr[0].(map[string]any); ok {
					if hp, ok := bind["HostPort"].(string); ok {
						if hostPort, err := strconv.Atoi(hp); err == nil {
							info.Ports[containerPort] = hostPort
						}
					}
				}
			}
		}
	}

	return info, nil
}
