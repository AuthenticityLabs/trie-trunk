package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListTaps(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zebra", "alpha", ".trunk", "Invalid", "bad name"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	taps, err := ListTaps(context.Background(), root)
	if err != nil || !reflect.DeepEqual(taps, []string{"alpha", "zebra"}) {
		t.Fatal(taps, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListTaps(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestValidateTap(t *testing.T) {
	for _, name := range []string{"0", "json", "payments.api-v2_test", strings.Repeat("x", 64)} {
		if err := ValidateTap(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", ".hidden", "a/b", `a\b`, "a%2fb", "a b", "A", "é", "a\x00b", strings.Repeat("x", 65)} {
		if err := ValidateTap(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}
