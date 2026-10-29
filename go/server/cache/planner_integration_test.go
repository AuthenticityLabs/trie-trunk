package cache

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchPlannerWorkerAgainstHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-runtime HTTP integration")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node test runner not installed")
	}
	l := testLayout(t, false)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(49 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for n := uint32(1); n <= 4; n++ {
		stamp := at.Add(time.Duration((n-1)/2) * time.Hour).Add(time.Duration(n) * time.Second)
		if _, err = p.api.Store.Append(Write, testFrame(t, l, stamp, n, false)); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.api.Store.Flush(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(p.Handler(""))
	defer server.Close()
	if target := os.Getenv("TRUNK_PLANNER_BROWSER_URL"); target != "" {
		if err := os.WriteFile(target, []byte(server.URL), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Println("Browser fixture ready:", server.URL)
		timeout := time.After(8 * time.Minute)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-timeout:
				t.Fatal("browser verification timed out")
			case <-tick.C:
				if _, err := os.Stat(target + ".done"); err == nil {
					return
				}
			}
		}
	}
	script, err := filepath.Abs("../../../client/scripts/test-planner-server.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--experimental-strip-types", script, server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worker integration: %v\n%s", err, output)
	}
	t.Log(string(output))
}
