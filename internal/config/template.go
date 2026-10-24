package config

import (
	"fmt"
	"regexp"
	"slices"
)

var placeholder = regexp.MustCompile(`\$\{([^}]+)\}`)

// Placeholders returns the names referenced as ${Name} in cmd, in order of first use.
func Placeholders(cmd string) []string {
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(cmd, -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	return names
}

// ExpandCommand splits cmd into arguments without a shell, then replaces ${Name} inside each
// argument with lookup(Name). A substituted value always stays inside its argument, whatever
// characters it contains.
func ExpandCommand(cmd string, lookup func(name string) (string, bool)) ([]string, error) {
	args, err := splitCommand(cmd)
	if err != nil {
		return nil, err
	}
	var missing []string
	for i, a := range args {
		args[i] = placeholder.ReplaceAllStringFunc(a, func(m string) string {
			name := placeholder.FindStringSubmatch(m)[1]
			v, ok := lookup(name)
			if !ok {
				missing = append(missing, name)
			}
			return v
		})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no value for %q", missing)
	}
	return args, nil
}
