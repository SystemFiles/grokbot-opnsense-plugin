package main

// The committed bin/*.gz must match bin/SHA256SUMS, and (when built with the
// same toolchain) the host target rebuilt from this source must be
// byte-identical, so the prebuilt binaries are provably this code.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sums(t *testing.T) map[string]string {
	b, err := os.ReadFile("../../bin/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		m[f[1]] = f[0]
	}
	return m
}

func TestCommittedBinariesMatchChecksums(t *testing.T) {
	want := sums(t)
	if len(want) != 5 {
		t.Fatalf("expected 5 targets, got %d", len(want))
	}
	for name, sum := range want {
		f, err := os.Open(filepath.Join("../../bin", name+".gz"))
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		_, _ = io.Copy(h, zr)
		f.Close()
		if got := hex.EncodeToString(h.Sum(nil)); got != sum {
			t.Errorf("%s.gz unpacks to %s, SHA256SUMS says %s", name, got, sum)
		}
	}
}

func TestHostBinaryReproducesFromSource(t *testing.T) {
	gv, _ := os.ReadFile("../../bin/GOVERSION")
	if strings.TrimSpace(string(gv)) != runtime.Version() {
		t.Skipf("binaries built with %s, this is %s", strings.TrimSpace(string(gv)), runtime.Version())
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	name := "opnsense-mcp-" + runtime.GOOS + "-" + runtime.GOARCH + ext
	want, ok := sums(t)[name]
	if !ok {
		t.Skipf("no prebuilt target for %s", name)
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -buildid=", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	b, _ := os.ReadFile(out)
	got := sha256.Sum256(b)
	if !bytes.Equal([]byte(hex.EncodeToString(got[:])), []byte(want)) {
		t.Fatalf("committed %s is stale: rebuild gives %x, SHA256SUMS has %s (run make build)", name, got, want)
	}
}
