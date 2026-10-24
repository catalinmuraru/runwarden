package executor

import (
	"slices"
	"testing"
)

func TestMasker(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		chunks  []string
		want    string
	}{
		{"no secrets", nil, []string{"plain ", "text"}, "plain text"},
		{"whole secret", []string{"secret"}, []string{"a secret b"}, "a ****** b"},
		{"split across chunks", []string{"secret"}, []string{"xx sec", "ret yy"}, "xx ****** yy"},
		{"split over three chunks", []string{"secret"}, []string{"se", "cr", "et!"}, "******!"},
		{"secret at the very end", []string{"secret"}, []string{"end secr", "et"}, "end ******"},
		{"longest first", []string{"ab", "abcd"}, []string{"abcd ab"}, "****** ******"},
		{"blank secrets ignored", []string{"", "  "}, []string{"a  b"}, "a  b"},
		{"repeated", []string{"pw"}, []string{"pwpw", "pw"}, "******************"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMasker(tt.secrets)
			var got []byte
			for _, c := range tt.chunks {
				got = append(got, m.write([]byte(c))...)
			}
			got = append(got, m.flush()...)
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvPolicy(t *testing.T) {
	t.Setenv("PATH", "/custom/bin")
	t.Setenv("LANG", "")
	t.Setenv("OTHER", "not allowed")
	t.Setenv("RUNNER_ENV_TOKEN", "for-scripts")
	t.Setenv("CUSTOM_EXPOSE_X", "custom")

	env, refused := EnvPolicy{}.withDefaults().build(map[string]string{
		"PARAM_A":       "1",
		"PATH":          "/evil",
		"LD_PRELOAD":    "x",
		"BASH_FUNC_x%%": "() { :; }",
	})
	for _, want := range []string{"PATH=/custom/bin", "LANG=C.UTF-8", "TOKEN=for-scripts", "PARAM_A=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q: %q", want, env)
		}
	}
	for _, kv := range env {
		if kv == "OTHER=not allowed" || kv == "LD_PRELOAD=x" || kv == "PATH=/evil" {
			t.Errorf("env contains %q", kv)
		}
	}
	if want := []string{"BASH_FUNC_x%%", "LD_PRELOAD", "PATH"}; !slices.Equal(refused, want) {
		t.Errorf("refused = %q, want %q", refused, want)
	}

	env, _ = EnvPolicy{Allow: []string{"OTHER"}, Prefix: "CUSTOM_EXPOSE_"}.withDefaults().build(nil)
	for _, want := range []string{"OTHER=not allowed", "X=custom", "PATH=" + DefaultPath} {
		if !slices.Contains(env, want) {
			t.Errorf("custom policy env missing %q: %q", want, env)
		}
	}
}
