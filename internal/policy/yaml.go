package policy

import (
	"bytes"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

// ExportYAML encodes the policy for download (without the revision).
func ExportYAML(p *Policy) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Runwarden access policy. Import it in the Admin area; it replaces the whole policy.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ImportYAML decodes an exported policy. Unknown fields are refused, so typos do not silently
// drop rules. Exceptions that have already expired are not audited again.
func ImportYAML(data []byte, now time.Time) (*Policy, error) {
	var p Policy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	for i := range p.Exceptions {
		p.Exceptions[i].ExpiryAudited = !p.Exceptions[i].Active(now)
	}
	p.normalize()
	return &p, nil
}

// Summary describes what replacing old with new changes, for the import preview.
type Summary struct {
	Environments int `json:"environments"`
	Roles        int `json:"roles"`
	Users        int `json:"users"`
	Rules        int `json:"rules"`
	Exceptions   int `json:"exceptions"`
	// Removed counts what exists now but not in the imported policy.
	RemovedRules      int `json:"removed_rules"`
	RemovedExceptions int `json:"removed_exceptions"`
	RemovedUsers      int `json:"removed_users"`
}

// Summarize compares the imported policy with the current one.
func Summarize(current, imported *Policy) Summary {
	s := Summary{Environments: len(imported.Environments), Roles: len(imported.Roles), Users: len(imported.Users),
		Rules: len(imported.Rules), Exceptions: len(imported.Exceptions)}
	for _, r := range current.Rules {
		if _, ok := imported.FindRule(r.ID); !ok {
			s.RemovedRules++
		}
	}
	for _, e := range current.Exceptions {
		if _, ok := imported.FindException(e.ID); !ok {
			s.RemovedExceptions++
		}
	}
	for _, u := range current.Users {
		if imported.RolesOf(u.Name) == nil {
			s.RemovedUsers++
		}
	}
	return s
}
