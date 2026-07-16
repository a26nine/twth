package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestVersionFlagBypassesConfiguration(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "--version")
	cmd.Env = append(os.Environ(), "UPSTREAM_URL=://invalid")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run . --version error = %v, output = %q", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "rpc-proxy version=dev commit=unknown"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}
