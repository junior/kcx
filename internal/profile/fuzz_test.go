package profile

import (
	"strings"
	"testing"
)

// FuzzRender: a template renders without panicking, and a value goes in as it
// is: braces in a cluster's name or a user's are never expanded a second time.
func FuzzRender(f *testing.F) {
	for _, s := range [][2]string{
		{"{name}.k8s.example", "prod1"},
		{"{env.ACME_USER}", "jane"},
		{"{user}@{name}", "{host}"},
		{"{nope}", "x"},
		{"{{name}}", "{out}"},
		{"", ""},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, tmpl, value string) {
		v := Vars{Name: value, Host: value, User: value, Out: value, Getenv: func(string) string { return value }}
		_, _ = Render(tmpl, v)
		got, err := Render("{name}|{user}|{env.X}", v)
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.Join([]string{value, value, value}, "|"); got != want {
			t.Fatalf("value %q rendered as %q", value, got)
		}
	})
}
