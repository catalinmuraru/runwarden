package params

import (
	"maps"
	"slices"
	"testing"
)

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		p    Parameter
		v    Value
		want []string
	}{
		{"positional scalar", Parameter{Type: TypeText}, Scalar("x"), []string{"x"}},
		{"param and value", Parameter{Param: "--a"}, Scalar("x"), []string{"--a", "x"}},
		{"same arg param", Parameter{Param: "--a=", SameArgParam: true}, Scalar("x"), []string{"--a=x"}},
		{"value with spaces stays one argument", Parameter{Param: "--a"}, Scalar("x y; rm -rf /"), []string{"--a", "x y; rm -rf /"}},
		{"empty scalar", Parameter{Param: "--a"}, Scalar(""), nil},
		{"missing value", Parameter{Param: "--a"}, Value{}, nil},
		{"flag true", Parameter{Param: "--f", NoValue: true}, Flag(true), []string{"--f"}},
		{"flag false", Parameter{Param: "--f", NoValue: true}, Flag(false), nil},
		{"flag without param", Parameter{NoValue: true}, Flag(true), nil},
		{"multiselect default separator", Parameter{Type: TypeMultiselect, Param: "--m"}, List("a", "b"), []string{"--m", "a,b"}},
		{"multiselect custom separator positional", Parameter{Type: TypeMultiselect, Separator: " "}, List("a", "b"), []string{"a b"}},
		{"argument per value", Parameter{Type: TypeMultiselect, Param: "--m", ArgMode: ArgPerValue}, List("a", "b"), []string{"--m", "a", "b"}},
		{"argument per value same arg", Parameter{Type: TypeMultiselect, Param: "--m=", ArgMode: ArgPerValue, SameArgParam: true}, List("a", "b"), []string{"--m=a", "b"}},
		{"argument per value positional", Parameter{Type: TypeMultiselect, ArgMode: ArgPerValue}, List("a", "b"), []string{"a", "b"}},
		{"repeat param", Parameter{Type: TypeMultiselect, Param: "--m", ArgMode: ArgRepeatParam}, List("a", "b"), []string{"--m", "a", "--m", "b"}},
		{"repeat param same arg", Parameter{Type: TypeMultiselect, Param: "--m=", ArgMode: ArgRepeatParam, SameArgParam: true}, List("a", "b"), []string{"--m=a", "--m=b"}},
		{"empty list", Parameter{Type: TypeMultiselect, Param: "--m", ArgMode: ArgRepeatParam}, List(), nil},
		{"server file", Parameter{Type: TypeServerFile, FileDir: "/srv"}, Scalar("x.log"), []string{"/srv/x.log"}},
		{"recursive server file", Parameter{Type: TypeServerFile, FileDir: "/srv", FileRecursive: true}, List("a", "b.txt"), []string{"/srv/a/b.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.p.Name = "p"
			got := Build([]*Parameter{&tt.p}, map[string]Value{"p": tt.v}, "1").Args
			if !slices.Equal(got, tt.want) {
				t.Errorf("args = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOrderConstantAndPassAs(t *testing.T) {
	ps := []*Parameter{
		{Name: "first", Param: "-1"},
		{Name: "const", Param: "--c", Constant: true, Default: Scalar("fixed")},
		{Name: "env only", Param: "--e", PassAs: PassEnv},
		{Name: "stdin", Param: "--s", PassAs: PassStdin, StdinExpectedText: "Days?"},
		{Name: "arg only", Param: "--a", PassAs: PassArgument},
		{Name: "last"},
	}
	values := map[string]Value{
		"first":    Scalar("1"),
		"const":    Scalar("user tried to override"),
		"env only": Scalar("e"),
		"stdin":    Scalar("14"),
		"arg only": Scalar("a"),
		"last":     Scalar("z"),
	}
	inv := Build(ps, values, "42")

	wantArgs := []string{"-1", "1", "--c", "fixed", "--a", "a", "z"}
	if !slices.Equal(inv.Args, wantArgs) {
		t.Errorf("args = %q, want %q", inv.Args, wantArgs)
	}
	wantEnv := map[string]string{
		"PARAM_FIRST":    "1",
		"PARAM_CONST":    "fixed",
		"PARAM_ENV_ONLY": "e",
		"PARAM_LAST":     "z",
		"EXECUTION_ID":   "42",
	}
	if !maps.Equal(inv.Env, wantEnv) {
		t.Errorf("env = %v, want %v", inv.Env, wantEnv)
	}
	wantStdin := []StdinInput{{Value: "14", ExpectText: "Days?"}}
	if !slices.Equal(inv.Stdin, wantStdin) {
		t.Errorf("stdin = %v, want %v", inv.Stdin, wantStdin)
	}
}

func TestMasking(t *testing.T) {
	ps := []*Parameter{
		{Name: "key", Param: "--key", Secure: true},
		{Name: "level", Param: "--level=", SameArgParam: true, Secure: true},
		{Name: "joined", Param: "--j", Type: TypeMultiselect, Secure: true},
		{Name: "each", Param: "--e", Type: TypeMultiselect, ArgMode: ArgPerValue, Secure: true},
		{Name: "public", Param: "--p"},
	}
	values := map[string]Value{
		"key":    Scalar("s3cr3t"),
		"level":  Scalar("9"),
		"joined": List("a", "b"),
		"each":   List("c", "d"),
		"public": Scalar("visible"),
	}
	inv := Build(ps, values, "1")

	wantArgs := []string{"--key", "s3cr3t", "--level=9", "--j", "a,b", "--e", "c", "d", "--p", "visible"}
	wantMasked := []string{"--key", Mask, "--level=" + Mask, "--j", Mask, "--e", Mask, Mask, "--p", "visible"}
	if !slices.Equal(inv.Args, wantArgs) {
		t.Errorf("args = %q, want %q", inv.Args, wantArgs)
	}
	if !slices.Equal(inv.MaskedArgs, wantMasked) {
		t.Errorf("masked = %q, want %q", inv.MaskedArgs, wantMasked)
	}
	for _, s := range []string{"s3cr3t", "9", "a,b", "a", "b", "c", "d"} {
		if !slices.Contains(inv.SecureValues, s) {
			t.Errorf("SecureValues %q missing %q", inv.SecureValues, s)
		}
	}
	if slices.Contains(inv.SecureValues, "visible") {
		t.Error("non-secure value listed as secure")
	}
}

func TestEnvName(t *testing.T) {
	tests := map[string]string{
		"Simple Int":  "PARAM_SIMPLE_INT",
		"Dry run":     "PARAM_DRY_RUN",
		"a  -  b":     "PARAM_A_B",
		"x__y":        "PARAM_X_Y",
		"Tenant?":     "PARAM_TENANT_",
		"Café":        "PARAM_CAF_",
		"?!":          "",
		"":            "",
		"keep_days_2": "PARAM_KEEP_DAYS_2",
	}
	for in, want := range tests {
		if got := EnvName(in); got != want {
			t.Errorf("EnvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnv(t *testing.T) {
	ps := []*Parameter{
		{Name: "list", Type: TypeMultiselect},
		{Name: "on", NoValue: true},
		{Name: "off", NoValue: true},
		{Name: "explicit", EnvVar: "MY_VAR"},
		{Name: "a b"},
		{Name: "a-b"},
		{Name: "???"},
		{Name: "exec", EnvVar: "EXECUTION_ID"},
	}
	values := map[string]Value{
		"list":     List("x", "y"),
		"on":       Flag(true),
		"off":      Flag(false),
		"explicit": Scalar("v"),
		"a b":      Scalar("1"),
		"a-b":      Scalar("2"),
		"???":      Scalar("3"),
		"exec":     Scalar("custom"),
	}
	got := Build(ps, values, "42").Env
	want := map[string]string{
		"PARAM_ON":     "true",
		"MY_VAR":       "v",
		"EXECUTION_ID": "custom",
	}
	if !maps.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestNormalize(t *testing.T) {
	env := &Parameter{Name: "env", Type: TypeList, ValuesUIMapping: map[string]string{"production": "Production"}}
	if v, _ := env.Normalize("Production"); !eq(v, Scalar("production")) {
		t.Errorf("UI label not mapped back: %+v", v)
	}
	if v, _ := env.Normalize("other"); !eq(v, Scalar("other")) {
		t.Errorf("unmapped value changed: %+v", v)
	}

	multi := &Parameter{Name: "m", Type: TypeMultiselect}
	if v, _ := multi.Normalize("single"); !slices.Equal(v.List, []string{"single"}) || v.Kind != KindList {
		t.Errorf("single string not wrapped in list: %+v", v)
	}
	if v, _ := multi.Normalize([]any{"a", 2.0}); !slices.Equal(v.List, []string{"a", "2"}) {
		t.Errorf("list = %+v", v)
	}

	flag := &Parameter{Name: "f", NoValue: true}
	if v, _ := flag.Normalize("TRUE"); !eq(v, Flag(true)) {
		t.Errorf("string flag = %+v", v)
	}
	if _, err := flag.Normalize(3.0); err == nil {
		t.Error("number accepted as flag")
	}

	constant := &Parameter{Name: "c", Constant: true, Default: Scalar("fixed")}
	if v, _ := constant.Normalize("override"); !eq(v, Scalar("fixed")) {
		t.Errorf("constant = %+v", v)
	}

	num := &Parameter{Name: "n", Type: TypeInt}
	if v, _ := num.Normalize(5.0); !eq(v, Scalar("5")) {
		t.Errorf("number = %+v", v)
	}
	if _, err := num.Normalize(map[string]any{}); err == nil {
		t.Error("object accepted as value")
	}
}

func FuzzBuild(f *testing.F) {
	f.Add("a", "b", "--m", true, uint8(0))
	f.Add("", "x y", "", false, uint8(2))
	f.Fuzz(func(t *testing.T, a, b, param string, same bool, mode uint8) {
		modes := []ArgMode{ArgSingle, ArgPerValue, ArgRepeatParam}
		p := &Parameter{
			Name:         "m",
			Param:        param,
			Type:         TypeMultiselect,
			SameArgParam: same,
			ArgMode:      modes[int(mode)%len(modes)],
			Secure:       true,
		}
		inv := Build([]*Parameter{p}, map[string]Value{"m": List(a, b)}, "1")
		if len(inv.Args) > 4 {
			t.Fatalf("too many args: %q", inv.Args)
		}
		if len(inv.Args) != len(inv.MaskedArgs) {
			t.Fatalf("masked args out of line: %q vs %q", inv.Args, inv.MaskedArgs)
		}
	})
}

func eq(a, b Value) bool {
	return a.Kind == b.Kind && a.Str == b.Str && a.Flag == b.Flag && slices.Equal(a.List, b.List)
}
