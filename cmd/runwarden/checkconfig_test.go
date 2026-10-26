package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckConfigExamples(t *testing.T) {
	t.Chdir("../..") // example paths are relative to the repo root
	var out bytes.Buffer
	if code := checkConfig([]string{"examples/runners"}, &out); code != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "5 scripts, 1 warnings, 0 errors") || !strings.Contains(out.String(), "secrets DB_PASSWORD, API_TOKEN") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}
}

func TestCheckConfigUsage(t *testing.T) {
	var out bytes.Buffer
	if code := checkConfig(nil, &out); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if code := checkConfig([]string{"/does/not/exist"}, &out); code != 1 {
		t.Errorf("exit code %d for a missing dir, want 1", code)
	}
}
