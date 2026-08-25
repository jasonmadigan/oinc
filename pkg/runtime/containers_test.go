package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dockerBridgeInspect = `[
  {
    "NetworkSettings": {
      "IPAddress": "172.17.0.2",
      "IPPrefixLen": 16,
      "Networks": {
        "bridge": {
          "IPAddress": "172.17.0.2",
          "IPPrefixLen": 16
        }
      }
    }
  }
]`

const podmanInspect = `[
  {
    "NetworkSettings": {
      "IPAddress": "",
      "IPPrefixLen": 0,
      "Networks": {
        "podman": {
          "IPAddress": "10.88.0.4",
          "IPPrefixLen": 16
        }
      }
    }
  }
]`

const multiNetworkInspect = `[
  {
    "NetworkSettings": {
      "IPAddress": "",
      "IPPrefixLen": 0,
      "Networks": {
        "none-yet": {
          "IPAddress": "",
          "IPPrefixLen": 0
        },
        "custom": {
          "IPAddress": "192.168.42.9",
          "IPPrefixLen": 24
        }
      }
    }
  }
]`

const dualStackInspect = `[
  {
    "NetworkSettings": {
      "IPAddress": "",
      "IPPrefixLen": 0,
      "Networks": {
        "dual": {
          "IPAddress": "172.20.0.3",
          "IPPrefixLen": 24,
          "GlobalIPv6Address": "fd00:cafe::3",
          "GlobalIPv6PrefixLen": 64
        }
      }
    }
  }
]`

func TestSubnetFromInspect(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
	}{
		{"docker default bridge", dockerBridgeInspect, "172.17.0.0/16"},
		{"podman network map", podmanInspect, "10.88.0.0/16"},
		{"user-defined network", multiNetworkInspect, "192.168.42.0/24"},
		{"dual-stack network derives from IPv4", dualStackInspect, "172.20.0.0/24"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := subnetFromInspect([]byte(tt.input))
			if err != nil {
				t.Fatalf("subnetFromInspect: %v", err)
			}
			if got != tt.want {
				t.Errorf("subnet = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubnetFromInspectErrors(t *testing.T) {
	for _, tt := range []struct {
		name, input string
	}{
		{"no address", `[{"NetworkSettings":{"IPAddress":"","Networks":{}}}]`},
		{"zero prefix", `[{"NetworkSettings":{"IPAddress":"10.0.0.2","IPPrefixLen":0,"Networks":{}}}]`},
		{"oversized prefix", `[{"NetworkSettings":{"IPAddress":"10.0.0.2","IPPrefixLen":40,"Networks":{}}}]`},
		{"empty result", `[]`},
		{"not json", `nope`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := subnetFromInspect([]byte(tt.input)); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCreateContainerConfiguresPodmanForNestedContainers(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	fakePodman := filepath.Join(dir, "podman")
	if err := os.WriteFile(fakePodman, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OINC_TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatalf("writing fake podman: %v", err)
	}
	t.Setenv("OINC_TEST_ARGS", argsPath)

	rt := &Runtime{binary: fakePodman}
	if err := rt.CreateContainer(ContainerOpts{
		Name:             "oinc",
		Image:            "example.test/oinc:latest",
		NestedContainers: true,
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("reading podman arguments: %v", err)
	}
	if !strings.Contains(string(args), "\n--log-driver=k8s-file\n") {
		t.Fatalf("podman arguments did not select k8s-file logging:\n%s", args)
	}
	if !strings.Contains(string(args), "\n--tmpfs=/var/lib/containers\n") {
		t.Fatalf("podman arguments did not isolate nested container storage:\n%s", args)
	}
}
