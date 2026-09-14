package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chef"
)

func TestParseRunListItem(t *testing.T) {
	cases := map[string]RunListItem{
		"role[web]":            {Kind: "role", Name: "web"},
		"recipe[app::default]": {Kind: "recipe", Name: "app::default"},
		"recipe[app]":          {Kind: "recipe", Name: "app"},
		"app::x":               {Kind: "recipe", Name: "app::x"},
		"app":                  {Kind: "recipe", Name: "app"},
		"recipe[app::x@1.2.3]": {Kind: "recipe", Name: "app::x"},
		"  recipe[spaced]  ":   {Kind: "recipe", Name: "spaced"},
	}
	for in, want := range cases {
		if got := ParseRunListItem(in); got != want {
			t.Errorf("%q: got %+v want %+v", in, got, want)
		}
	}
	if (RunListItem{Kind: "recipe", Name: "app::x"}).Cookbook() != "app" {
		t.Error("Cookbook()")
	}
}

func TestConstraints(t *testing.T) {
	cases := []struct {
		constraint string
		version    string
		match      bool
	}{
		{">= 1.0", "1.0.0", true}, {">= 1.0", "0.9.9", false},
		{"~> 1.2", "1.9.0", true}, {"~> 1.2", "2.0.0", false}, {"~> 1.2", "1.1.0", false},
		{"~> 1.2.3", "1.2.9", true}, {"~> 1.2.3", "1.3.0", false},
		{"= 2.0.0", "2.0.0", true}, {"2.0.0", "2.0.0", true}, {"2.0.0", "2.0.1", false},
		{"< 2.0", "1.99.99", true}, {"<= 2.0", "2.0.0", true}, {"> 2.0", "2.0.0", false},
		{"", "0.0.1", true},
	}
	for _, c := range cases {
		con, err := ParseConstraint(c.constraint)
		if err != nil {
			t.Fatalf("%q: %v", c.constraint, err)
		}
		v, _ := ParseVersion(c.version)
		if con.Match(v) != c.match {
			t.Errorf("%q vs %s: got %v", c.constraint, c.version, !c.match)
		}
	}
	if best, ok := Best([]string{"1.0.0", "1.2.0", "2.0.0", "junk"}, "~> 1.0"); !ok || best != "1.2.0" {
		t.Errorf("Best: %s %v", best, ok)
	}
	if _, ok := Best([]string{"1.0.0"}, ">= 3.0"); ok {
		t.Error("Best should fail")
	}
	vs := []string{"1.10.0", "1.9.0", "1.2.3", "x"}
	SortVersions(vs)
	if strings.Join(vs, " ") != "1.2.3 1.9.0 1.10.0 x" {
		t.Errorf("SortVersions: %v", vs)
	}
	for _, bad := range []string{"1", "a.b", "1.2.3.4", ">= x"} {
		if _, err := ParseConstraint(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestExpand(t *testing.T) {
	roles := map[string]string{
		"base": `{"run_list":["role[web]","recipe[base::x]"],"env_run_lists":{"prod":["recipe[base::prod]"]}}`,
		"web":  `{"run_list":["recipe[app]","role[base]"]}`,
	}
	fetch := func(ctx context.Context, name string) (json.RawMessage, error) {
		r, ok := roles[name]
		if !ok {
			return nil, &chef.APIError{Status: 404}
		}
		return json.RawMessage(r), nil
	}
	ex, err := Expand(context.Background(), []string{"role[base]", "recipe[app]", "role[missing]"}, "", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ex.Recipes); got != "[app base::x]" {
		t.Errorf("recipes: %s", got)
	}
	if got := fmt.Sprint(ex.Roles); got != "[base web missing]" {
		t.Errorf("roles: %s", got)
	}
	if got := fmt.Sprint(ex.Missing); got != "[missing]" {
		t.Errorf("missing: %s", got)
	}
	ex, _ = Expand(context.Background(), []string{"role[base]"}, "prod", fetch)
	if got := fmt.Sprint(ex.Recipes); got != "[base::prod]" {
		t.Errorf("env run list: %s", got)
	}
}
