package params

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Kind tells which field of a Value is set.
type Kind uint8

// Value kinds.
const (
	KindNone Kind = iota
	KindScalar
	KindList
	KindFlag
)

// Value is a parameter value: a scalar, a list (multiselect, recursive server_file) or a flag.
type Value struct {
	Kind Kind
	Str  string
	List []string
	Flag bool
}

// Scalar returns a scalar value.
func Scalar(s string) Value { return Value{Kind: KindScalar, Str: s} }

// List returns a list value.
func List(items ...string) Value { return Value{Kind: KindList, List: items} }

// Flag returns a flag value.
func Flag(b bool) Value { return Value{Kind: KindFlag, Flag: b} }

// IsEmpty reports whether v carries nothing to pass to the script.
func (v Value) IsEmpty() bool {
	switch v.Kind {
	case KindScalar:
		return v.Str == ""
	case KindList:
		return len(v.List) == 0
	case KindFlag:
		return !v.Flag
	}
	return true
}

// Normalize converts a value received from a client (decoded JSON: string, number, bool, list
// or nil) into a Value for p. UI labels are mapped back to script values; constants always
// yield their default. The result still needs Validate.
func (p *Parameter) Normalize(raw any) (Value, error) {
	if p.Constant {
		return p.Default, nil
	}
	return p.convert(raw, true)
}

// ParseDefault converts a default from a runner config into a Value for p.
func (p *Parameter) ParseDefault(raw any) (Value, error) {
	return p.convert(raw, false)
}

func (p *Parameter) convert(raw any, mapUI bool) (Value, error) {
	if raw == nil {
		return Value{}, nil
	}
	if p.NoValue {
		b, err := toBool(raw)
		if err != nil {
			return Value{}, err
		}
		return Flag(b), nil
	}
	if p.IsList() {
		var items []string
		switch r := raw.(type) {
		case []any:
			for _, e := range r {
				s, err := toString(e)
				if err != nil {
					return Value{}, err
				}
				items = append(items, s)
			}
		case []string:
			items = append(items, r...)
		default:
			s, err := toString(raw)
			if err != nil {
				return Value{}, err
			}
			if s != "" {
				items = []string{s}
			}
		}
		if mapUI && p.Type == TypeMultiselect {
			for i, s := range items {
				items[i] = p.fromUILabel(s)
			}
		}
		return List(items...), nil
	}
	s, err := toString(raw)
	if err != nil {
		return Value{}, err
	}
	if mapUI {
		s = p.fromUILabel(s)
	}
	return Scalar(s), nil
}

// fromUILabel maps a label shown in the UI back to its script value.
func (p *Parameter) fromUILabel(label string) string {
	for script, ui := range p.ValuesUIMapping {
		if ui == label {
			return script
		}
	}
	return label
}

func toString(raw any) (string, error) {
	switch r := raw.(type) {
	case string:
		return r, nil
	case json.Number:
		return r.String(), nil
	case float64:
		return strconv.FormatFloat(r, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(r), nil
	case int64:
		return strconv.FormatInt(r, 10), nil
	case bool:
		return strconv.FormatBool(r), nil
	}
	return "", fmt.Errorf("unsupported value type %T", raw)
}

func toBool(raw any) (bool, error) {
	switch r := raw.(type) {
	case bool:
		return r, nil
	case string:
		return strings.EqualFold(strings.TrimSpace(r), "true"), nil
	}
	return false, fmt.Errorf("flag value must be true or false, got %T", raw)
}
