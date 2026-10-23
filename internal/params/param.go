// Package params models script parameters: their types, validation of user values, and how
// values are handed to a script as arguments, environment variables or stdin.
package params

import "regexp"

// Type is a parameter type as written in runner configs.
type Type string

// Parameter types. A flag is any parameter with NoValue set.
const (
	TypeText          Type = "text"
	TypeInt           Type = "int"
	TypeList          Type = "list"
	TypeMultiselect   Type = "multiselect"
	TypeEditableList  Type = "editable_list"
	TypeFileUpload    Type = "file_upload"
	TypeServerFile    Type = "server_file"
	TypeMultilineText Type = "multiline_text"
	TypeIP            Type = "ip"
	TypeIP4           Type = "ip4"
	TypeIP6           Type = "ip6"
)

// KnownType reports whether t is a supported parameter type.
func KnownType(t Type) bool {
	switch t {
	case TypeText, TypeInt, TypeList, TypeMultiselect, TypeEditableList, TypeFileUpload,
		TypeServerFile, TypeMultilineText, TypeIP, TypeIP4, TypeIP6:
		return true
	}
	return false
}

// ArgMode controls how a multiselect value becomes command-line arguments.
type ArgMode string

// Multiselect argument modes.
const (
	ArgSingle      ArgMode = "single_argument"    // one argument, values joined by Separator
	ArgPerValue    ArgMode = "argument_per_value" // param once, then one argument per value
	ArgRepeatParam ArgMode = "repeat_param_value" // param repeated before every value
)

// PassAs selects how a value reaches the script.
type PassAs string

// Pass modes. The zero value passes the value both as an argument and as an env variable.
const (
	PassDefault  PassAs = ""
	PassArgument PassAs = "argument"
	PassEnv      PassAs = "env_variable"
	PassStdin    PassAs = "stdin"
)

func (p PassAs) asArgument() bool { return p == PassDefault || p == PassArgument }
func (p PassAs) asEnv() bool      { return p == PassDefault || p == PassEnv }

// FileType restricts what a server_file parameter may select. Empty allows both.
type FileType string

// Server file types.
const (
	FileTypeFile FileType = "file"
	FileTypeDir  FileType = "dir"
)

// Parameter is one script parameter, fully parsed from a runner config.
type Parameter struct {
	Name        string
	Param       string // option name such as "--level"; empty means positional
	Type        Type
	Description string
	Required    bool

	Default       Value  // script value used when the user gives none (and for constants)
	DefaultScript string // command producing the default; run later, never through a shell
	Constant      bool
	Secure        bool

	NoValue      bool // flag: Param is passed alone when the value is true
	SameArgParam bool // "--opt=value" as one argument instead of "--opt" "value"
	ArgMode      ArgMode
	Separator    string
	PassAs       PassAs
	EnvVar       string

	Min, Max         *int64
	MaxLength        *int
	Regex            *regexp.Regexp // anchored: must match the whole value
	RegexDescription string

	Values          []string          // static allowed values (list, multiselect)
	ValuesScript    string            // command producing allowed values; may reference ${Other}
	ValuesUIMapping map[string]string // script value -> label shown in the UI

	FileDir        string
	FileRecursive  bool
	FileType       FileType
	FileExtensions []string
	ExcludedFiles  []string

	StdinExpectedText string // write the stdin value once the script prints this text
}

// IsList reports whether values of p are lists.
func (p *Parameter) IsList() bool {
	return p.Type == TypeMultiselect || (p.Type == TypeServerFile && p.FileRecursive)
}
