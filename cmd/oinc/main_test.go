package main

import (
	"io"
	"testing"

	"github.com/jasonmadigan/oinc/pkg/oinc"
	"github.com/spf13/cobra"
)

func TestConsoleBrandingFlagValidatesBeforeRunning(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantRun bool
	}{
		{"okd", true},
		{"redhat", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Cleanup(func() { flagConsoleBranding = oinc.DefaultConsoleBranding })
			ran := false
			cmd := &cobra.Command{Use: "create", RunE: func(*cobra.Command, []string) error {
				ran = true
				return nil
			}}
			addConsoleBrandingFlag(cmd)
			cmd.SetArgs([]string{"--console-branding", tc.value})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if ran != tc.wantRun || (err == nil) != tc.wantRun {
				t.Fatalf("--console-branding %s: ran=%v err=%v", tc.value, ran, err)
			}
			if tc.wantRun && string(flagConsoleBranding) != tc.value {
				t.Fatalf("flag stored %q, want %q", flagConsoleBranding, tc.value)
			}
		})
	}
}
