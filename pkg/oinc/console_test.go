package oinc

import "testing"

func TestResolvePluginURL(t *testing.T) {
	tests := []struct {
		name          string
		spec          string
		containerHost string
		want          string
	}{
		{
			name:          "rewrite podman host to docker",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://host.docker.internal:9001",
		},
		{
			name:          "rewrite docker host to podman",
			spec:          "my-plugin=http://host.docker.internal:9001",
			containerHost: "host.containers.internal",
			want:          "my-plugin=http://host.containers.internal:9001",
		},
		{
			name:          "already correct docker host",
			spec:          "my-plugin=http://host.docker.internal:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://host.docker.internal:9001",
		},
		{
			name:          "already correct podman host",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "host.containers.internal",
			want:          "my-plugin=http://host.containers.internal:9001",
		},
		{
			name:          "localhost left alone",
			spec:          "my-plugin=http://localhost:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://localhost:9001",
		},
		{
			name:          "linux host rewrite",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "localhost",
			want:          "my-plugin=http://localhost:9001",
		},
		{
			name:          "no url part",
			spec:          "my-plugin",
			containerHost: "host.docker.internal",
			want:          "my-plugin",
		},
		{
			name:          "empty spec",
			spec:          "",
			containerHost: "host.docker.internal",
			want:          "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePluginURL(tt.spec, tt.containerHost)
			if got != tt.want {
				t.Errorf("resolvePluginURL(%q, %q) = %q, want %q", tt.spec, tt.containerHost, got, tt.want)
			}
		})
	}
}

func TestConsoleProxyServiceName(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		alias      string
		want       string
	}{
		{
			name:       "ordinary plugin and alias",
			pluginName: "kuadrant-console-plugin",
			alias:      "backend",
			want:       "oinc-kuadrant-console-plugin-backend",
		},
		{
			name:       "normalizes unsupported characters",
			pluginName: "Example_Plugin",
			alias:      "MCP.Backend",
			want:       "oinc-example-plugin-mcp-backend",
		},
		{
			name:       "long names have a stable collision-resistant suffix",
			pluginName: "a-console-plugin-name-that-is-deliberately-much-too-long-for-dns",
			alias:      "a-long-backend-alias",
			want:       "oinc-a-console-plugin-name-that-is-deliberately-much-t-1f5cd6b5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := consoleProxyServiceName(tt.pluginName, tt.alias)
			if got != tt.want {
				t.Fatalf("consoleProxyServiceName(%q, %q) = %q, want %q", tt.pluginName, tt.alias, got, tt.want)
			}
			if len(got) > 63 {
				t.Fatalf("service name is %d characters, want at most 63", len(got))
			}
		})
	}
}
