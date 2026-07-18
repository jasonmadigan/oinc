package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
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

func (r *Runtime) ImageExists(image string) bool {
	_, err := r.run("image", "inspect", image)
	return err == nil
}

// StreamImageToContainer pipes `save <image>` into `exec -i <container> <args...>`.
// Both ends' exit statuses are checked; a failed save is reported first so it
// does not surface as a confusing error from the consuming command.
func (r *Runtime) StreamImageToContainer(image, container string, execArgs ...string) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}

	save := r.command("save", image)
	save.Stdout = pw
	var saveStderr bytes.Buffer
	save.Stderr = &saveStderr

	consume := r.command(append([]string{"exec", "-i", container}, execArgs...)...)
	consume.Stdin = pr
	var consumeStderr bytes.Buffer
	consume.Stderr = &consumeStderr

	if err := save.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("%s save %s: %w", r.binary, image, err)
	}
	if err := consume.Start(); err != nil {
		// drop both parent ends so save hits EPIPE and can be reaped
		pr.Close()
		pw.Close()
		_ = save.Wait()
		return fmt.Errorf("%s exec %s: %w", r.binary, strings.Join(execArgs, " "), err)
	}

	// the children hold their own dups; closing the parent ends lets
	// each side see EOF/EPIPE when the other exits
	pw.Close()
	pr.Close()

	saveErr := save.Wait()
	consumeErr := consume.Wait()

	// a consumer failure kills save with SIGPIPE; report the consumer's
	// error then, so the root cause is not masked by a broken pipe
	if consumeErr != nil && (saveErr == nil || killedBySIGPIPE(saveErr)) {
		return fmt.Errorf("%s exec %s: %w\n%s", r.binary, strings.Join(execArgs, " "), consumeErr, consumeStderr.String())
	}
	if saveErr != nil {
		return fmt.Errorf("%s save %s: %w\n%s", r.binary, image, saveErr, saveStderr.String())
	}
	return nil
}

func killedBySIGPIPE(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGPIPE
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

// ContainerSubnet returns the IPv4 subnet (CIDR) of the network the named
// container is attached to, derived from the container's own address.
func (r *Runtime) ContainerSubnet(name string) (string, error) {
	out, err := r.run("inspect", name)
	if err != nil {
		return "", err
	}
	return subnetFromInspect(out)
}

// subnetFromInspect derives the container's IPv4 subnet from inspect JSON.
// docker's default bridge reports the address at the NetworkSettings top
// level; podman and user-defined networks report it per-network.
func subnetFromInspect(data []byte) (string, error) {
	var raw []struct {
		NetworkSettings struct {
			IPAddress   string `json:"IPAddress"`
			IPPrefixLen int    `json:"IPPrefixLen"`
			Networks    map[string]struct {
				IPAddress   string `json:"IPAddress"`
				IPPrefixLen int    `json:"IPPrefixLen"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("parsing inspect output: %w", err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("empty inspect result")
	}

	ns := raw[0].NetworkSettings
	addr, prefix := ns.IPAddress, ns.IPPrefixLen
	if addr == "" {
		// sorted for determinism when attached to several networks
		names := make([]string, 0, len(ns.Networks))
		for n := range ns.Networks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if nw := ns.Networks[n]; nw.IPAddress != "" {
				addr, prefix = nw.IPAddress, nw.IPPrefixLen
				break
			}
		}
	}
	if addr == "" {
		return "", fmt.Errorf("container has no network address")
	}
	// a zero or out-of-range prefix would yield a subnet like 0.0.0.0/0
	if prefix <= 0 || prefix > 32 {
		return "", fmt.Errorf("container address %s has unusable prefix length %d", addr, prefix)
	}

	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("container address %q is not IPv4", addr)
	}
	mask := net.CIDRMask(prefix, 32)
	subnet := net.IPNet{IP: ip.Mask(mask), Mask: mask}
	return subnet.String(), nil
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
