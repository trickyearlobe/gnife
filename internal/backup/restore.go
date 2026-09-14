package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

// RestoreOptions control Restore.
type RestoreOptions struct {
	Options
	// OrgDir selects organizations/<OrgDir>; empty picks the only one, or
	// the one matching the destination organisation.
	OrgDir    string
	CreateOrg bool
	// ValidatorKeyFile receives the validator key of an organisation created
	// by CreateOrg; it must not already exist.
	ValidatorKeyFile string
	Overwrite        bool
	Force            bool
	Purge            bool
	DryRun           bool
}

// RestoreResult is what Restore did.
type RestoreResult struct {
	Org     string           `json:"organization"`
	From    string           `json:"from"`
	Users   []string         `json:"users"`
	Members []string         `json:"members"`
	Result  *transfer.Result `json:"result,omitempty"`
	// ValidatorKeyfile is where a newly created organisation's validator key went.
	ValidatorKeyfile string         `json:"validator_keyfile,omitempty"`
	Purged           []string       `json:"purged"`
	Plan             map[string]any `json:"plan,omitempty"`
	Failed           cli.ItemErrors `json:"failed"`
}

// Err returns a partial error when anything failed.
func (r *RestoreResult) Err() error {
	var all cli.ItemErrors
	all = append(all, r.Failed...)
	if r.Result != nil {
		all = append(all, r.Result.Failed...)
	}
	if len(all) == 0 {
		return nil
	}
	return cli.Partial(all)
}

// Restore writes a backup at root into the organisation dst is bound to.
func Restore(ctx context.Context, dst *chef.Client, root string, opts RestoreOptions) (*RestoreResult, error) {
	opts.defaults()
	org := dst.Org()
	if org == "" {
		return nil, fmt.Errorf("destination profile has no organisation")
	}
	orgDir, err := pickOrgDir(root, opts.OrgDir, org)
	if err != nil {
		return nil, err
	}
	src := &DirSource{OrgDir: orgDir}
	res := &RestoreResult{Org: org, From: orgDir, Users: []string{}, Members: []string{}, Purged: []string{}, Failed: cli.ItemErrors{}}
	fail := func(item string, err error) {
		res.Failed = append(res.Failed, cli.ItemError{Item: item, Err: err.Error()})
	}

	items, err := src.Items()
	if err != nil {
		return nil, err
	}
	var selected []transfer.Item
	for _, it := range items {
		if opts.Wants(it.Kind) {
			selected = append(selected, it)
		}
	}
	plan := &transfer.Plan{Items: selected}
	if !opts.SkipACLs {
		acls, err := src.ACLItems()
		if err != nil {
			return nil, err
		}
		for _, a := range acls {
			if a.Kind == nil || opts.Wants(a.Kind) {
				plan.ACLs = append(plan.ACLs, a)
			}
		}
	}

	users, _ := listJSONNames(filepath.Join(root, "users"))
	sort.Strings(users)
	members := readMembers(filepath.Join(orgDir, "members.json"))
	if users == nil {
		users = []string{}
	}
	if members == nil {
		members = []string{}
	}

	if opts.DryRun {
		res.Plan = plan.Summary()
		res.Plan["users"] = users
		res.Plan["members"] = members
		if opts.Purge {
			purge, err := purgeList(ctx, dst, selected, opts)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(purge))
			for _, p := range purge {
				names = append(names, p.String())
			}
			res.Plan["purge"] = names
		}
		return res, nil
	}

	// Organisation.
	keyFile, err := ensureOrg(ctx, dst, orgDir, opts)
	if err != nil {
		return nil, err
	}
	res.ValidatorKeyfile = keyFile

	// Server-level users, then org membership. Both need a superuser.
	if !opts.SkipUsers && len(users) > 0 {
		opts.Progress("server: %d users", len(users))
		forbidden := false
		for _, u := range users {
			if u == "pivotal" {
				continue // the destination's superuser is never touched
			}
			body, err := readJSON(filepath.Join(root, "users", u+".json"))
			if err != nil {
				fail("user "+u, err)
				continue
			}
			id := kinds.ID{Name: u}
			exists, err := kinds.User.Exists(ctx, dst, id)
			if chef.IsNotPermitted(err) {
				forbidden = true
				break
			}
			if err != nil {
				fail("user "+u, err)
				continue
			}
			if exists && !opts.Overwrite {
				continue
			}
			if err := kinds.User.Put(ctx, dst, id, body, exists); err != nil {
				if chef.IsNotPermitted(err) {
					forbidden = true
					break
				}
				fail("user "+u, err)
				continue
			}
			res.Users = append(res.Users, u)
			if keys, err := src.Keys(ctx, kinds.User, u); err == nil && keys != nil {
				if err := kinds.PutKeys(ctx, dst, kinds.User, u, keys); err != nil {
					fail("user keys "+u, err)
				}
			}
			if aclBody, err := readJSON(filepath.Join(root, "user_acls", u+".json")); err == nil && !opts.SkipACLs {
				var a acl.ACL
				if json.Unmarshal(aclBody, &a) == nil {
					if err := acl.Put(ctx, dst, "/users/"+u+"/_acl", a); err != nil {
						fail("user acl "+u, err)
					}
				}
			}
		}
		if forbidden {
			opts.Warn("users: not permitted for this profile; skipping users/ (use a pivotal profile or --skip-users)")
		}
	}
	if !opts.SkipUsers && len(members) > 0 {
		opts.Progress("%s: %d members", org, len(members))
		for _, u := range members {
			err := dst.Post(ctx, dst.OrgPath("/users"), map[string]string{"username": u}, nil)
			switch {
			case err == nil:
				res.Members = append(res.Members, u)
			case chef.IsConflict(err):
			case chef.IsNotPermitted(err):
				opts.Warn("members: not permitted for this profile; skipping membership (use a pivotal profile or --skip-users)")
				u = ""
			default:
				fail("member "+u, err)
			}
			if u == "" {
				break
			}
		}
	}

	// Objects and ACLs.
	res.Result = transfer.Execute(ctx, src, dst, plan, transfer.ExecOptions{
		Overwrite: opts.Overwrite, Force: opts.Force, Workers: opts.Workers,
		Progress: opts.Progress, Warn: opts.Warn,
	})

	if opts.Purge {
		purge, err := purgeList(ctx, dst, selected, opts)
		if err != nil {
			return res, err
		}
		opts.Progress("%s: purging %d objects", org, len(purge))
		errs := cli.ForEach(ctx, opts.Workers, purge, func(p purgeItem) string { return p.String() }, func(ctx context.Context, p purgeItem) error {
			return p.kind.Delete(ctx, dst, p.id)
		})
		for _, p := range purge {
			res.Purged = append(res.Purged, p.String())
		}
		res.Failed = append(res.Failed, errs...)
	}
	return res, nil
}

// pickOrgDir chooses organizations/<name> under root.
func pickOrgDir(root, want, dstOrg string) (string, error) {
	dirs, err := listDirs(filepath.Join(root, "organizations"))
	if err != nil {
		return "", err
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("%s: no organizations/ directory; is this a backup?", root)
	}
	if want != "" {
		for _, d := range dirs {
			if d == want {
				return OrgDir(root, d), nil
			}
		}
		return "", fmt.Errorf("%s: no organizations/%s (have: %s)", root, want, strings.Join(dirs, ", "))
	}
	if len(dirs) == 1 {
		return OrgDir(root, dirs[0]), nil
	}
	for _, d := range dirs {
		if d == dstOrg {
			return OrgDir(root, d), nil
		}
	}
	return "", fmt.Errorf("%s holds several organisations (%s); pick one with --org", root, strings.Join(dirs, ", "))
}

// ensureOrg checks the destination organisation exists, creating it from
// org.json (under the destination name) when asked. It returns the path the
// new validator key was saved to, or "".
func ensureOrg(ctx context.Context, dst *chef.Client, orgDir string, opts RestoreOptions) (string, error) {
	org := dst.Org()
	err := dst.Get(ctx, "/organizations/"+org, nil)
	switch {
	case err == nil:
		return "", nil
	case chef.IsNotPermitted(err):
		// Members without org-read still work against the org; assume it exists.
		return "", nil
	case !chef.IsNotFound(err):
		return "", err
	}
	if !opts.CreateOrg {
		return "", fmt.Errorf("organisation '%s' does not exist on %s (use --create-org with a superuser profile)", org, dst.ServerRoot())
	}
	if opts.ValidatorKeyFile == "" {
		return "", fmt.Errorf("--create-org needs somewhere to save the validator key")
	}
	if _, err := os.Stat(opts.ValidatorKeyFile); err == nil {
		return "", fmt.Errorf("%s already exists; move it or pass --validator-keyfile", opts.ValidatorKeyFile)
	}
	full := org
	if body, err := readJSON(filepath.Join(orgDir, "org.json")); err == nil {
		if f := kinds.Field(body, "full_name"); f != "" {
			full = f
		}
	}
	opts.Progress("server: creating organisation %s", org)
	created, err := kinds.CreateOrganization(ctx, dst, org, full)
	if err != nil {
		return "", err
	}
	if created.PrivateKey == "" {
		return "", nil
	}
	if err := kinds.SaveValidatorKey(opts.ValidatorKeyFile, created.PrivateKey); err != nil {
		// Print rather than lose it.
		fmt.Fprintf(os.Stderr, "%s\n", created.PrivateKey)
		return "", fmt.Errorf("writing %s: %w (the key was printed above)", opts.ValidatorKeyFile, err)
	}
	return opts.ValidatorKeyFile, nil
}

func readMembers(path string) []string {
	body, err := readJSON(path)
	if err != nil {
		return nil
	}
	var members []struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &members) != nil {
		return nil
	}
	var names []string
	for _, m := range members {
		if m.User.Username != "" {
			names = append(names, m.User.Username)
		}
	}
	sort.Strings(names)
	return names
}

type purgeItem struct {
	kind *kinds.Kind
	id   kinds.ID
}

func (p purgeItem) String() string { return p.kind.Name + " " + p.id.String() }

// protected objects are never purged.
func protected(k *kinds.Kind, id kinds.ID, self string) bool {
	switch k {
	case kinds.Environment:
		return id.Name == "_default"
	case kinds.Group:
		switch id.Name {
		case "admins", "billing-admins", "clients", "users", "public_key_read_access":
			return true
		}
		return kinds.IsUSAG(id.Name)
	case kinds.Container:
		return true
	case kinds.Client:
		return id.Name == self
	}
	return false
}

// purgeList finds destination objects, of the kinds present in the backup,
// that the backup does not contain.
func purgeList(ctx context.Context, dst *chef.Client, items []transfer.Item, opts RestoreOptions) ([]purgeItem, error) {
	have := map[string]bool{}
	kindsPresent := map[*kinds.Kind]bool{}
	for _, it := range items {
		have[it.Kind.Name+":"+it.ID.String()] = true
		kindsPresent[it.Kind] = true
	}
	var out []purgeItem
	for _, k := range kinds.Restorable() {
		if !kindsPresent[k] || k == kinds.Organization || k == kinds.User || !opts.Wants(k) {
			continue
		}
		ids, err := k.List(ctx, dst)
		if err != nil {
			return nil, fmt.Errorf("listing %s for purge: %w", k.Plural, err)
		}
		for _, id := range ids {
			if have[k.Name+":"+id.String()] || protected(k, id, dst.ClientName()) {
				continue
			}
			out = append(out, purgeItem{k, id})
		}
	}
	// Delete dependants before the things they depend on: reverse restore order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].kind.Order > out[j].kind.Order })
	return out, nil
}

// Exists reports whether root looks like a backup directory.
func Exists(root string) bool {
	st, err := os.Stat(filepath.Join(root, "organizations"))
	return err == nil && st.IsDir()
}
