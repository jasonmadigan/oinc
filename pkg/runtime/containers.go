package runtime

import (
	"fmt"
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
}

type PortMapping struct {
	Host      int
	Container int
	BindIP    string // empty = 127.0.0.1
}

func (r *Runtime) PullImage(image string) error {
	// check if already present
	if _, err := r.run("image", "inspect", image); err == nil {
		return nil
	}
	_, err := r.run("pull", image)
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
		args = append(args, "-it", "--privileged")
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

func (r *Runtime) NetworkSubnet(network string) (string, error) {
	out, err := r.run("network", "inspect", network, "-f", "{{range .IPAM.Config}}{{.Subnet}}{{end}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
