package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// flexBool accepts true/false or a string; strings are true only when equal to "true"
// (any case), matching how existing configs write booleans.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*b = false
	case bool:
		*b = flexBool(t)
	case string:
		*b = flexBool(strings.EqualFold(strings.TrimSpace(t), "true"))
	default:
		return fmt.Errorf("expected true or false, got %s", data)
	}
	return nil
}

// flexInt accepts an integer or a string holding one.
type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return fmt.Errorf("expected an integer, got %s", data)
	}
	*n = flexInt(v)
	return nil
}

// flexDuration accepts a Go duration string ("90m", "4h") or a number of minutes.
type flexDuration time.Duration

func (d *flexDuration) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case float64:
		*d = flexDuration(time.Duration(t * float64(time.Minute)))
	case string:
		s := strings.TrimSpace(t)
		if m, err := strconv.ParseFloat(s, 64); err == nil {
			*d = flexDuration(time.Duration(m * float64(time.Minute)))
			return nil
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("expected a duration like \"90m\" or a number of minutes, got %s", data)
		}
		*d = flexDuration(parsed)
	default:
		return fmt.Errorf("expected a duration like \"90m\" or a number of minutes, got %s", data)
	}
	return nil
}

// stringOrList accepts "a" or ["a", "b"].
type stringOrList []string

func (l *stringOrList) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*l = nil
	case string:
		*l = stringOrList{t}
	case []any:
		out := make(stringOrList, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("expected a list of strings, got %s", data)
			}
			out = append(out, s)
		}
		*l = out
	default:
		return fmt.Errorf("expected a string or a list of strings, got %s", data)
	}
	return nil
}

type rawScheduling struct {
	Enabled flexBool `json:"enabled"`
}

type rawScript struct {
	Name             string            `json:"name"`
	ScriptPath       string            `json:"script_path"`
	WorkingDirectory string            `json:"working_directory"`
	Description      string            `json:"description"`
	Group            string            `json:"group"`
	AllowedUsers     json.RawMessage   `json:"allowed_users"` // ignored: rules decide access
	AdminUsers       json.RawMessage   `json:"admin_users"`   // ignored: rules decide access
	Tags             stringOrList      `json:"tags"`
	Secrets          stringOrList      `json:"secrets"`
	EnvParams        map[string]string `json:"environment_parameters"`
	OutputFiles      []string          `json:"output_files"`
	RedactPatterns   []string          `json:"redact_patterns"`
	RequiresTerminal *flexBool         `json:"requires_terminal"`
	Timeout          *flexDuration     `json:"timeout"`
	Scheduling       *rawScheduling    `json:"scheduling"`
	Hidden           flexBool          `json:"hidden"`
	Include          stringOrList      `json:"include"`
	Parameters       []json.RawMessage `json:"parameters"`
}

type rawRegex struct {
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
}

type rawParam struct {
	Name                    string            `json:"name"`
	Param                   string            `json:"param"`
	Type                    string            `json:"type"`
	Description             string            `json:"description"`
	Required                flexBool          `json:"required"`
	Default                 json.RawMessage   `json:"default"`
	Constant                flexBool          `json:"constant"`
	Secure                  flexBool          `json:"secure"`
	NoValue                 flexBool          `json:"no_value"`
	SameArgParam            flexBool          `json:"same_arg_param"`
	MultiselectArgumentType string            `json:"multiselect_argument_type"`
	MultipleArguments       *flexBool         `json:"multiple_arguments"`
	Separator               *string           `json:"separator"`
	PassAs                  string            `json:"pass_as"`
	EnvVar                  string            `json:"env_var"`
	Min                     *flexInt          `json:"min"`
	Max                     *flexInt          `json:"max"`
	MaxLength               *flexInt          `json:"max_length"`
	Regex                   *rawRegex         `json:"regex"`
	Values                  json.RawMessage   `json:"values"`
	ValuesUIMapping         map[string]string `json:"values_ui_mapping"`
	FileDir                 string            `json:"file_dir"`
	FileRecursive           flexBool          `json:"file_recursive"`
	FileType                string            `json:"file_type"`
	FileExtensions          []string          `json:"file_extensions"`
	ExcludedFiles           []string          `json:"excluded_files"`
	StdinExpectedText       string            `json:"stdin_expected_text"`
}

// decodeAny decodes JSON keeping numbers as json.Number, so "5" and 5 render the same.
func decodeAny(data json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// scriptRef extracts the command from {"script": "..."}; ok is false for other shapes.
func scriptRef(v any) (cmd string, ok bool) {
	m, isMap := v.(map[string]any)
	if !isMap {
		return "", false
	}
	s, isString := m["script"].(string)
	return s, isString
}

// splitCommand splits a command line into arguments without a shell: whitespace separates,
// single quotes are literal, double quotes allow \" and \\, a backslash escapes outside quotes.
func splitCommand(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inArg = true, true
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote or escape")
	}
	if inArg {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}
	return args, nil
}

// usesShellSyntax reports constructs that only a shell would interpret.
func usesShellSyntax(cmd string) bool {
	for _, tok := range []string{"|", "&&", ";", ">", "<", "$(", "`"} {
		if strings.Contains(cmd, tok) {
			return true
		}
	}
	return false
}
