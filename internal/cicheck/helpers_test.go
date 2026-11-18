package cicheck

import (
	"os"
	"path/filepath"
	"testing"
)

func absRoot(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(root(t, parts...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
