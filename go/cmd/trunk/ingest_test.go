package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestRejectsIncompleteOrRemoteLocalConfig(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name  string
		args  []string
		token string
		want  string
	}{
		{name: "wildcard", args: []string{"--host", "0.0.0.0"}, want: "loopback"},
		{name: "IPv6 wildcard", args: []string{"--host", "::"}, want: "loopback"},
		{name: "remote", args: []string{"--host", "192.0.2.1"}, want: "loopback"},
		{name: "hostname", args: []string{"--host", "logs.example"}, want: "loopback"},
		{name: "token only", token: "test-token", want: "both --cert and --key"},
		{name: "certificate only", args: []string{"--cert", "missing.pem"}, want: "both --cert and --key"},
		{name: "key only", args: []string{"--key", "missing.pem"}, want: "both --cert and --key"},
		{name: "CA only", args: []string{"--client-ca", "missing.pem"}, want: "both --cert and --key"},
		{name: "HTTP3", args: []string{"--transport", "http3"}, want: "both --cert and --key"},
		{name: "TLS without auth", args: []string{"--cert", "missing.pem", "--key", "missing.pem"}, want: "TRUNK_TOKEN or --client-ca"},
		{name: "bad certificate", args: []string{"--cert", "missing.pem", "--key", "missing.pem"}, token: "test-token", want: "missing.pem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRUNK_TOKEN", tc.token)
			root := filepath.Join(t.TempDir(), "logs")
			args := append([]string{"--dir", root}, tc.args...)
			if err := runIngest(args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created storage: %v", err)
			}
		})
	}
}
