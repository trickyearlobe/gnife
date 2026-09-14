// Package deps resolves what an object needs: run lists to roles and
// recipes, recipes to cookbook versions (via the server's depsolver), and
// cookbook version constraints.
package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/trickyearlobe/gnife/internal/chef"
)

// RunListItem is a parsed run list entry.
type RunListItem struct {
	Kind string // "role" or "recipe"
	Name string // role name, or cookbook[::recipe]
}

// ParseRunListItem accepts role[x], recipe[x::y], recipe[x], x::y and x.
func ParseRunListItem(s string) RunListItem {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "role[") && strings.HasSuffix(s, "]") {
		return RunListItem{Kind: "role", Name: s[5 : len(s)-1]}
	}
	if strings.HasPrefix(s, "recipe[") && strings.HasSuffix(s, "]") {
		s = s[7 : len(s)-1]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 { // recipe[foo@1.0.0] version pins
		s = s[:i]
	}
	return RunListItem{Kind: "recipe", Name: s}
}

// Cookbook returns the cookbook part of a recipe name.
func (r RunListItem) Cookbook() string {
	name, _, _ := strings.Cut(r.Name, "::")
	return name
}

// String renders the item in run list form.
func (r RunListItem) String() string { return r.Kind + "[" + r.Name + "]" }

// Role is the subset of a role body needed for expansion.
type Role struct {
	RunList     []string            `json:"run_list"`
	EnvRunLists map[string][]string `json:"env_run_lists"`
}

// RoleFetcher returns a role body by name.
type RoleFetcher func(ctx context.Context, name string) (json.RawMessage, error)

// Expansion is the result of expanding a run list.
type Expansion struct {
	Recipes []string // in order, deduplicated, as bare cookbook[::recipe] names (the depsolver's form)
	Roles   []string // every role visited, in first-seen order
	Missing []string // roles that could not be fetched
}

// Expand walks roles recursively (using env_run_lists[env] when present)
// and returns the recipes and roles a run list resolves to.
func Expand(ctx context.Context, runList []string, env string, fetch RoleFetcher) (*Expansion, error) {
	ex := &Expansion{}
	seenRecipe := map[string]bool{}
	seenRole := map[string]bool{}
	var walk func(items []string) error
	walk = func(items []string) error {
		for _, raw := range items {
			it := ParseRunListItem(raw)
			switch it.Kind {
			case "recipe":
				if !seenRecipe[it.Name] {
					seenRecipe[it.Name] = true
					ex.Recipes = append(ex.Recipes, it.Name)
				}
			case "role":
				if seenRole[it.Name] {
					continue
				}
				seenRole[it.Name] = true
				ex.Roles = append(ex.Roles, it.Name)
				body, err := fetch(ctx, it.Name)
				if err != nil {
					if chef.IsNotFound(err) {
						ex.Missing = append(ex.Missing, it.Name)
						continue
					}
					return err
				}
				var r Role
				if err := json.Unmarshal(body, &r); err != nil {
					return fmt.Errorf("role %s: %w", it.Name, err)
				}
				list := r.RunList
				if env != "" && env != "_default" {
					if l, ok := r.EnvRunLists[env]; ok {
						list = l
					}
				}
				if err := walk(list); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(runList); err != nil {
		return nil, err
	}
	return ex, nil
}

// Solve asks the server's depsolver which cookbook versions the recipes
// resolve to in the environment (transitive dependencies included).
func Solve(ctx context.Context, c *chef.Client, env string, recipes []string) (map[string]string, error) {
	if env == "" {
		env = "_default"
	}
	if len(recipes) == 0 {
		return map[string]string{}, nil
	}
	var out map[string]struct {
		Version string `json:"version"`
	}
	path := c.OrgPath("/environments/" + url.PathEscape(env) + "/cookbook_versions")
	if err := c.Post(ctx, path, map[string]any{"run_list": recipes}, &out); err != nil {
		return nil, fmt.Errorf("depsolver (%s): %w", env, err)
	}
	res := make(map[string]string, len(out))
	for name, v := range out {
		res[name] = v.Version
	}
	return res, nil
}

// Version is a Chef cookbook version: x.y or x.y.z.
type Version struct {
	Major, Minor, Patch int
	HasPatch            bool
}

// ParseVersion parses "1.2" or "1.2.3".
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid version '%s'", s)
	}
	var v Version
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("invalid version '%s'", s)
		}
		nums[i] = n
	}
	v.Major, v.Minor = nums[0], nums[1]
	if len(nums) == 3 {
		v.Patch, v.HasPatch = nums[2], true
	}
	return v, nil
}

func (v Version) String() string {
	if v.HasPatch {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Compare returns -1, 0 or 1.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}

// Constraint is a Chef version constraint: = > < >= <= ~> followed by a version.
type Constraint struct {
	Op string
	V  Version
}

// ParseConstraint parses ">= 1.0", "~> 2.1.0", "= 1.2.3" or a bare "1.2.3" (meaning =).
func ParseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Constraint{Op: ">=", V: Version{}}, nil
	}
	for _, op := range []string{">=", "<=", "~>", "=", ">", "<"} {
		if strings.HasPrefix(s, op) {
			v, err := ParseVersion(strings.TrimSpace(s[len(op):]))
			if err != nil {
				return Constraint{}, fmt.Errorf("invalid constraint '%s': %w", s, err)
			}
			return Constraint{Op: op, V: v}, nil
		}
	}
	v, err := ParseVersion(s)
	if err != nil {
		return Constraint{}, fmt.Errorf("invalid constraint '%s': %w", s, err)
	}
	return Constraint{Op: "=", V: v}, nil
}

// Match reports whether v satisfies the constraint.
func (c Constraint) Match(v Version) bool {
	cmp := Compare(v, c.V)
	switch c.Op {
	case "=":
		return cmp == 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case "~>":
		if cmp < 0 {
			return false
		}
		if c.V.HasPatch {
			return v.Major == c.V.Major && v.Minor == c.V.Minor
		}
		return v.Major == c.V.Major
	}
	return false
}

// Best returns the highest of versions satisfying constraint.
func Best(versions []string, constraint string) (string, bool) {
	c, err := ParseConstraint(constraint)
	if err != nil {
		return "", false
	}
	var best string
	var bestV Version
	for _, s := range versions {
		v, err := ParseVersion(s)
		if err != nil || !c.Match(v) {
			continue
		}
		if best == "" || Compare(v, bestV) > 0 {
			best, bestV = s, v
		}
	}
	return best, best != ""
}

// Latest returns the highest version in the list.
func Latest(versions []string) (string, bool) { return Best(versions, ">= 0.0.0") }

// SortVersions sorts version strings ascending (unparseable ones last, lexically).
func SortVersions(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool {
		a, errA := ParseVersion(versions[i])
		b, errB := ParseVersion(versions[j])
		if errA != nil || errB != nil {
			if errA == nil {
				return true
			}
			if errB == nil {
				return false
			}
			return versions[i] < versions[j]
		}
		return Compare(a, b) < 0
	})
}
