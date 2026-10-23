package main

import (
	"fmt"
	"io"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// checkConfig implements `runwarden check-config <runners-dir>`: it loads every runner config,
// reports scripts, warnings and errors, and returns the process exit code.
func checkConfig(args []string, out io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(out, "usage: runwarden check-config <runners-dir>")
		return 2
	}
	cat, err := config.LoadDir(args[0], config.Options{})
	if err != nil {
		_, _ = fmt.Fprintln(out, "error:", err)
		return 1
	}
	for _, s := range cat.Scripts {
		_, _ = fmt.Fprintf(out, "ok       %s (%d parameters)\n", s.Name, len(s.Parameters))
	}
	for _, w := range cat.Warnings {
		_, _ = fmt.Fprintf(out, "warning  %s\n", w)
	}
	for _, e := range cat.Errors {
		_, _ = fmt.Fprintf(out, "error    %s\n", e)
	}
	_, _ = fmt.Fprintf(out, "\n%d scripts, %d warnings, %d errors\n", len(cat.Scripts), len(cat.Warnings), len(cat.Errors))
	if len(cat.Errors) > 0 {
		return 1
	}
	return 0
}
