package params

import (
	"fmt"
	"net/netip"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidationError describes an invalid value. It never contains the value itself, so it is
// safe to log for secure parameters.
type ValidationError struct {
	Param  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("parameter %q: %s", e.Param, e.Reason)
}

// Validate checks a normalized value against p's rules.
func (p *Parameter) Validate(v Value) error {
	if p.Constant {
		return nil
	}
	if v.IsEmpty() {
		if p.Required && !p.NoValue {
			return p.invalid("is required")
		}
		return nil
	}
	if p.NoValue {
		if v.Kind != KindFlag {
			return p.invalid("must be true or false")
		}
		return nil
	}
	if p.IsList() != (v.Kind == KindList) {
		if p.IsList() {
			return p.invalid("must be a list")
		}
		return p.invalid("must be a single value")
	}

	switch p.Type {
	case TypeInt:
		return p.validateInt(v.Str)
	case TypeList:
		return p.validateAllowed(v.Str)
	case TypeMultiselect:
		for _, item := range v.List {
			if err := p.validateAllowed(item); err != nil {
				return err
			}
		}
		return nil
	case TypeIP, TypeIP4, TypeIP6:
		return p.validateIP(v.Str)
	case TypeServerFile:
		return p.validateServerFile(v)
	case TypeText, TypeMultilineText, TypeEditableList, "":
		return p.validateText(v.Str)
	}
	return nil
}

func (p *Parameter) invalid(format string, args ...any) error {
	return &ValidationError{Param: p.Name, Reason: fmt.Sprintf(format, args...)}
}

func (p *Parameter) validateInt(s string) error {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return p.invalid("must be an integer")
	}
	if p.Min != nil && n < *p.Min {
		return p.invalid("must be at least %d", *p.Min)
	}
	if p.Max != nil && n > *p.Max {
		return p.invalid("must be at most %d", *p.Max)
	}
	return nil
}

// validateAllowed checks membership in static values. Values produced by a script are checked
// by the caller, which knows the current list.
func (p *Parameter) validateAllowed(s string) error {
	if p.ValuesScript != "" || p.Values == nil {
		return nil
	}
	if !slices.Contains(p.Values, s) {
		return p.invalid("is not one of the allowed values")
	}
	return nil
}

func (p *Parameter) validateIP(s string) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return p.invalid("must be an IP address")
	}
	if p.Type == TypeIP4 && !addr.Is4() {
		return p.invalid("must be an IPv4 address")
	}
	if p.Type == TypeIP6 && !addr.Is6() {
		return p.invalid("must be an IPv6 address")
	}
	return nil
}

func (p *Parameter) validateText(s string) error {
	if p.MaxLength != nil && utf8.RuneCountInString(s) > *p.MaxLength {
		return p.invalid("must be at most %d characters", *p.MaxLength)
	}
	if p.Regex != nil && !p.Regex.MatchString(s) {
		desc := p.RegexDescription
		if desc == "" {
			desc = "the required format"
		}
		return p.invalid("must match %s", desc)
	}
	return nil
}

// validateServerFile checks the selection lexically: it must name an entry inside FileDir,
// with an allowed extension and not excluded. Existence is checked when the run starts.
func (p *Parameter) validateServerFile(v Value) error {
	segments := v.List
	if !p.FileRecursive {
		segments = []string{v.Str}
	}
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
			return p.invalid("contains an invalid path segment")
		}
	}
	rel := path.Join(segments...)

	if len(p.FileExtensions) > 0 && p.FileType != FileTypeDir {
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(rel), "."))
		ok := slices.ContainsFunc(p.FileExtensions, func(e string) bool {
			return strings.ToLower(strings.TrimPrefix(e, ".")) == ext
		})
		if !ok {
			return p.invalid("has a file extension that is not allowed")
		}
	}

	// Check every prefix too: an excluded directory excludes everything below it.
	for i := range segments {
		relPrefix := path.Join(segments[:i+1]...)
		fullPrefix := filepath.ToSlash(filepath.Join(p.FileDir, filepath.FromSlash(relPrefix)))
		for _, pattern := range p.ExcludedFiles {
			if excluded(pattern, segments[i], relPrefix, fullPrefix) {
				return p.invalid("is excluded")
			}
		}
	}
	return nil
}

// excluded matches an exclusion glob. Patterns without a slash match a single path segment;
// others match the relative or the full path. "**" spans directories.
func excluded(pattern, segment, rel, full string) bool {
	re, err := globToRegexp(pattern)
	if err != nil {
		return false
	}
	if !strings.Contains(pattern, "/") {
		return re.MatchString(segment)
	}
	return re.MatchString(rel) || re.MatchString(full)
}

func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	rs := []rune(glob)
	for i := 0; i < len(rs); i++ {
		rest := string(rs[i:])
		switch {
		case strings.HasPrefix(rest, "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(rest, "**"):
			b.WriteString(".*")
			i++
		case rs[i] == '*':
			b.WriteString("[^/]*")
		case rs[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
