package params

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Parameter
		v       Value
		wantErr bool
	}{
		{"required missing", Parameter{Required: true}, Value{}, true},
		{"required empty", Parameter{Required: true}, Scalar(""), true},
		{"required present", Parameter{Required: true}, Scalar("x"), false},
		{"required flag may be false", Parameter{Required: true, NoValue: true}, Flag(false), false},
		{"optional missing", Parameter{}, Value{}, false},
		{"constant ignores value", Parameter{Constant: true, Type: TypeInt}, Scalar("nope"), false},

		{"int ok", Parameter{Type: TypeInt}, Scalar("5"), false},
		{"int not a number", Parameter{Type: TypeInt}, Scalar("abc"), true},
		{"int below min", Parameter{Type: TypeInt, Min: ptr(int64(1))}, Scalar("0"), true},
		{"int above max", Parameter{Type: TypeInt, Max: ptr(int64(9))}, Scalar("10"), true},
		{"int at bounds", Parameter{Type: TypeInt, Min: ptr(int64(1)), Max: ptr(int64(9))}, Scalar("9"), false},

		{"regex match", Parameter{Regex: regexp.MustCompile(`^(?:[a-z]+)$`)}, Scalar("abc"), false},
		{"regex partial match rejected", Parameter{Regex: regexp.MustCompile(`^(?:[a-z]+)$`)}, Scalar("abc1"), true},
		{"max length", Parameter{MaxLength: ptr(3)}, Scalar("abcd"), true},
		{"max length counts runes", Parameter{MaxLength: ptr(3)}, Scalar("äöü"), false},

		{"list allowed", Parameter{Type: TypeList, Values: []string{"a", "b"}}, Scalar("b"), false},
		{"list not allowed", Parameter{Type: TypeList, Values: []string{"a", "b"}}, Scalar("c"), true},
		{"list from script not checked here", Parameter{Type: TypeList, ValuesScript: "list.sh"}, Scalar("c"), false},
		{"multiselect not allowed", Parameter{Type: TypeMultiselect, Values: []string{"a"}}, List("a", "z"), true},
		{"multiselect given scalar", Parameter{Type: TypeMultiselect}, Scalar("a"), true},
		{"text given list", Parameter{Type: TypeText}, List("a"), true},

		{"ip4 ok", Parameter{Type: TypeIP4}, Scalar("10.0.0.1"), false},
		{"ip4 rejects v6", Parameter{Type: TypeIP4}, Scalar("::1"), true},
		{"ip6 ok", Parameter{Type: TypeIP6}, Scalar("::1"), false},
		{"ip6 rejects v4", Parameter{Type: TypeIP6}, Scalar("10.0.0.1"), true},
		{"ip invalid", Parameter{Type: TypeIP}, Scalar("example.com"), true},

		{"server file ok", Parameter{Type: TypeServerFile, FileDir: "/srv"}, Scalar("a.log"), false},
		{"server file traversal", Parameter{Type: TypeServerFile, FileDir: "/srv"}, Scalar(".."), true},
		{"server file nested path", Parameter{Type: TypeServerFile, FileDir: "/srv"}, Scalar("../etc/passwd"), true},
		{"server file absolute", Parameter{Type: TypeServerFile, FileDir: "/srv"}, Scalar("/etc/passwd"), true},
		{"recursive traversal", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true}, List("a", "..", "b"), true},
		{"recursive ok", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true}, List("a", "b.log"), false},
		{"extension ok any case", Parameter{Type: TypeServerFile, FileDir: "/srv", FileExtensions: []string{".log"}}, Scalar("a.LOG"), false},
		{"extension not allowed", Parameter{Type: TypeServerFile, FileDir: "/srv", FileExtensions: []string{"log"}}, Scalar("a.txt"), true},
		{"excluded segment glob", Parameter{Type: TypeServerFile, FileDir: "/srv", ExcludedFiles: []string{"auth*"}}, Scalar("auth.log"), true},
		{"excluded directory excludes contents", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true, ExcludedFiles: []string{"**/tmp"}}, List("x", "tmp", "y.log"), true},
		{"excluded full path glob", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true, ExcludedFiles: []string{"/srv/**/user*"}}, List("a", "users.txt"), true},
		{"not excluded", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true, ExcludedFiles: []string{"**/tmp"}}, List("x", "y.log"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.p.Name = "p"
			err := tt.p.Validate(tt.v)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			var ve *ValidationError
			if err != nil && !errors.As(err, &ve) {
				t.Fatalf("error %T is not a *ValidationError", err)
			}
		})
	}
}

func TestValidationErrorOmitsValue(t *testing.T) {
	p := &Parameter{Name: "token", Secure: true, Regex: regexp.MustCompile(`^(?:[0-9]+)$`)}
	err := p.Validate(Scalar("hunter2"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the value: %v", err)
	}
}
