package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func clearTrunkEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("ENVIRONMENT", "")
	if err := os.Unsetenv("ENVIRONMENT"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DIR", "WEB", "PORT", "HOST", "TRANSPORT", "NETWORK", "FLUSH", "CERT", "KEY", "CLIENT_CA", "TOKEN", "ENTRY_HEADERS", "ENDPOINT", "SOCKET", "CA", "CLIENT_CERT", "CLIENT_KEY", "SERVE_PORT", "QUERIES", "HTTP_ADDRESS"} {
		key := "TRUNK_" + name
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func writeEnvironment(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadEnvironment(t *testing.T) {
	path := writeEnvironment(t, "# deployment\r\n\nexport TRUNK_DIR = './log files' # comment\nTRUNK_TOKEN=abc#def==\nTRUNK_KEY=\"C:\\keys\\server.pem\"\nTRUNK_CLIENT_CA=\nTRUNK_HOST=127.0.0.1\t# comment\nTRUNK_LITERAL='$(echo no) $HOME \\n'\n")
	got, err := readEnvironment(path, true)
	want := environment{"TRUNK_DIR": "./log files", "TRUNK_TOKEN": "abc#def==", "TRUNK_KEY": `C:\keys\server.pem`, "TRUNK_CLIENT_CA": "", "TRUNK_HOST": "127.0.0.1", "TRUNK_LITERAL": `$(echo no) $HOME \n`}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed configuration differs: %v", err)
	}
	for _, text := range []string{
		"TRUNK_TOKEN=secret\nnot-an-assignment\n",
		"TRUNK_TOKEN='secret\n",
		"TRUNK_TOKEN=\"secret\" trailing\n",
		"TRUNK_TOKEN=secret\nTRUNK_TOKEN=other\n",
		"BAD-NAME=secret\n",
		"9NAME=secret\n",
		"TRUNK_TOKEN=" + strings.Repeat("secret", 12000),
	} {
		if _, err := readEnvironment(writeEnvironment(t, text), true); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("missing or unredacted parse error: %v", err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.env")
	if _, err := readEnvironment(missing, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvironment(missing, true); err == nil {
		t.Fatal("explicit missing file accepted")
	}
	if _, err := readEnvironment("", true); err != nil {
		t.Fatal("disabled environment file", err)
	}
	if _, err := readEnvironment(t.TempDir(), false); err == nil {
		t.Fatal("unreadable file accepted")
	}
}

func TestEnvironmentPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, process, argument, want string
		hasProcess, hasArgument       bool
	}{
		{name: "file", want: "file-logs"},
		{name: "process", process: "process-logs", hasProcess: true, want: "process-logs"},
		{name: "argument", process: "process-logs", hasProcess: true, argument: "argument-logs", hasArgument: true, want: "argument-logs"},
		{name: "empty process", hasProcess: true, want: ""},
		{name: "empty argument", process: "process-logs", hasProcess: true, hasArgument: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTrunkEnvironment(t)
			path := writeEnvironment(t, "TRUNK_DIR=file-logs\nTRUNK_FLUSH=2s\nTRUNK_TOKEN=file-token\n")
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			flags.String("env", path, "")
			dir := flags.String("dir", "default-logs", "")
			flush := flags.Duration("flush", time.Second, "")
			if tc.hasProcess {
				t.Setenv("TRUNK_DIR", tc.process)
			}
			var args []string
			if tc.hasArgument {
				args = []string{"--dir=" + tc.argument}
			}
			if err := flags.Parse(args); err != nil {
				t.Fatal(err)
			}
			env, err := applyEnvironment(flags, "dir", "flush")
			if err != nil || *dir != tc.want || *flush != 2*time.Second {
				t.Fatal("wrong precedence", err)
			}
			if token, ok := env.lookup("TRUNK_TOKEN"); !ok || token != "file-token" {
				t.Fatal("token not loaded")
			}
			if _, ok := os.LookupEnv("TRUNK_TOKEN"); ok {
				t.Fatal("process environment mutated")
			}
			t.Setenv("TRUNK_TOKEN", "process-token")
			if token, _ := env.lookup("TRUNK_TOKEN"); token != "process-token" {
				t.Fatal("process token must override file token")
			}
		})
	}
}

func TestEnvironmentIngestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, file, want string
		args             []string
	}{
		{name: "port", file: "TRUNK_PORT=70000", want: "invalid port"},
		{name: "positional port wins", file: "TRUNK_PORT=invalid\nTRUNK_CERT=cert.pem", args: []string{"5140"}, want: "both --cert and --key"},
		{name: "partial TLS", file: "TRUNK_KEY=key.pem", want: "both --cert and --key"},
		{name: "token", file: "TRUNK_TOKEN=secret", want: "both --cert and --key"},
		{name: "client CA", file: "TRUNK_CLIENT_CA=ca.pem", want: "both --cert and --key"},
		{name: "TLS needs auth", file: "TRUNK_CERT=cert.pem\nTRUNK_KEY=key.pem", want: "TRUNK_TOKEN or --client-ca"},
		{name: "remote host", file: "TRUNK_HOST=0.0.0.0", want: "loopback"},
		{name: "HTTP3", file: "TRUNK_TRANSPORT=http3", want: "both --cert and --key"},
		{name: "invalid duration", file: "TRUNK_FLUSH=secret", want: "invalid TRUNK_FLUSH"},
		{name: "negative duration", file: "TRUNK_FLUSH=-1s", want: "must be positive"},
		{name: "flag wins over invalid duration", file: "TRUNK_FLUSH=secret\nTRUNK_CERT=cert.pem", args: []string{"--flush", "1s"}, want: "both --cert and --key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTrunkEnvironment(t)
			t.Chdir(t.TempDir())
			if err := os.WriteFile(".env", []byte(tc.file), 0600); err != nil {
				t.Fatal(err)
			}
			err := runIngest(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
			if _, err := os.Stat("logs"); !os.IsNotExist(err) {
				t.Fatal("invalid configuration touched storage")
			}
		})
	}
}

func TestEnvironmentFileSelection(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("invalid-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--env", "missing.env"}} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.String("env", ".env", "")
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		if _, err := applyEnvironment(flags); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("bad file accepted or contents exposed", err)
		}
	}
	for _, path := range []string{"", writeEnvironment(t, "TRUNK_HOST=127.0.0.1")} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.String("env", ".env", "")
		if err := flags.Parse([]string{"--env", path}); err != nil {
			t.Fatal(err)
		}
		if _, err := applyEnvironment(flags); err != nil {
			t.Fatal("selected file not respected", err)
		}
	}
}

func TestEnvironmentProfiles(t *testing.T) {
	clearTrunkEnvironment(t)
	t.Chdir(t.TempDir())
	for name, text := range map[string]string{
		".env":      "ENVIRONMENT=sand\nTRUNK_DIR=base-logs\nTRUNK_FLUSH=2s\nTRUNK_TOKEN=base-token\n",
		".env.sand": "TRUNK_DIR=sand-logs\nTRUNK_TOKEN=\nTRUNK_ENTRY_HEADERS=id,level\nENVIRONMENT=ignored\n",
		".env.prod": "TRUNK_DIR=prod-logs\n",
	} {
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	load := func(args ...string) (environment, string, error) {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.String("env", ".env", "")
		dir := flags.String("dir", "default-logs", "")
		if err := flags.Parse(args); err != nil {
			return nil, "", err
		}
		env, err := applyEnvironment(flags, "dir")
		return env, *dir, err
	}
	env, dir, err := load()
	if err != nil || dir != "sand-logs" || env["TRUNK_FLUSH"] != "2s" || env["TRUNK_TOKEN"] != "" || env["TRUNK_ENTRY_HEADERS"] != "id,level" {
		t.Fatal("base/profile overlay failed", err)
	}
	if token, _ := env.lookup("TRUNK_TOKEN"); token != "" {
		t.Fatal("empty profile value did not override base")
	}
	t.Setenv("TRUNK_DIR", "process-logs")
	if _, dir, err := load(); err != nil || dir != "process-logs" {
		t.Fatal("process environment lost precedence", err)
	}
	if _, dir, err := load("--dir=argument-logs"); err != nil || dir != "argument-logs" {
		t.Fatal("argument lost precedence", err)
	}
	if err := os.Unsetenv("TRUNK_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVIRONMENT", "prod")
	if _, dir, err := load(); err != nil || dir != "prod-logs" {
		t.Fatal("process selector did not override base selector", err)
	}
	if env, dir, err := load("--env=.env.sand"); err != nil || dir != "sand-logs" || env["TRUNK_FLUSH"] != "" {
		t.Fatal("explicit file should bypass base and automatic overlay", err)
	}
	t.Setenv("ENVIRONMENT", "")
	if _, dir, err := load(); err != nil || dir != "base-logs" {
		t.Fatal("empty selector did not disable profile", err)
	}
	for _, profile := range []string{"missing", "../escape", "prod/other", "prod\\other", "two words"} {
		t.Setenv("ENVIRONMENT", profile)
		if _, _, err := load(); err == nil {
			t.Fatal("accepted missing or invalid profile")
		}
		if _, dir, err := load("--env="); err != nil || dir != "default-logs" {
			t.Fatal("disabled file loading still selected a profile", err)
		}
	}
	t.Setenv("ENVIRONMENT", "sand")
	if err := os.Remove(".env"); err != nil {
		t.Fatal(err)
	}
	if _, dir, err := load(); err != nil || dir != "sand-logs" {
		t.Fatal("process selector should work without base file", err)
	}
}
