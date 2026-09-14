package chefserver

import "encoding/json"

// Seed fills an organisation with a small, interdependent estate used by
// the tests: two cookbooks (app depends on base), two roles (base-role
// nests web), an environment, a run-list node, a Policyfile node with its
// policy, artifact and group, a data bag, a group and a client.
func (s *Server) Seed(org string) {
	s.AddOrg(org)
	s.AddCookbook(org, "base", "1.0.0", map[string]string{
		"metadata.rb":        `name "base"`,
		"recipes/default.rb": "# base default",
		"recipes/x.rb":       "# base x",
	}, map[string]any{"dependencies": map[string]string{}})
	s.AddCookbook(org, "base", "1.1.0", map[string]string{
		"metadata.rb":        `name "base"`,
		"recipes/default.rb": "# base default 1.1",
		"recipes/x.rb":       "# base x 1.1",
	}, map[string]any{"dependencies": map[string]string{}})
	s.AddCookbook(org, "app", "2.0.0", map[string]string{
		"metadata.rb":                    `name "app"`,
		"recipes/default.rb":             "# app",
		"templates/default/app.conf.erb": "conf <%= @x %>",
		"files/default/static.txt":       "static",
	}, map[string]any{"dependencies": map[string]string{"base": ">= 1.0"}})
	s.AddCookbook(org, "unused", "0.1.0", map[string]string{
		"recipes/default.rb": "# unused",
	}, nil)
	s.AddArtifact(org, "app", "abc123", map[string]string{
		"recipes/default.rb": "# app artifact",
	}, map[string]any{"version": "2.0.0"})

	s.AddClient(org, "web-01", false)
	s.AddUser("alice", false)
	s.mu.Lock()
	o := s.Orgs[org]
	o.Members = append(o.Members, "alice")
	o.Roles["web"] = raw(map[string]any{"name": "web", "run_list": []string{"recipe[app]"}, "env_run_lists": map[string][]string{}, "json_class": "Chef::Role", "chef_type": "role", "default_attributes": map[string]any{}, "override_attributes": map[string]any{}})
	o.Roles["base-role"] = raw(map[string]any{"name": "base-role", "run_list": []string{"role[web]", "recipe[base::x]"}, "env_run_lists": map[string][]string{}, "json_class": "Chef::Role", "chef_type": "role"})
	o.Environments["prod"] = raw(map[string]any{"name": "prod", "cookbook_versions": map[string]string{"base": "= 1.0.0"}, "json_class": "Chef::Environment", "chef_type": "environment"})
	o.Nodes["web-01"] = raw(map[string]any{"name": "web-01", "chef_environment": "prod", "run_list": []string{"role[base-role]"}, "json_class": "Chef::Node", "chef_type": "node", "normal": map[string]any{"tags": []string{}}, "automatic": map[string]any{"platform": "ubuntu"}})
	o.Nodes["pol-01"] = raw(map[string]any{"name": "pol-01", "chef_environment": "_default", "run_list": []string{}, "policy_name": "demo", "policy_group": "dev", "json_class": "Chef::Node", "chef_type": "node"})
	o.Policies["demo"] = map[string]json.RawMessage{"rev1": raw(map[string]any{"name": "demo", "revision_id": "rev1", "run_list": []string{"recipe[app::default]"}, "cookbook_locks": map[string]any{"app": map[string]any{"identifier": "abc123", "version": "2.0.0"}}})}
	o.PolicyGroups["dev"] = map[string]string{"demo": "rev1"}
	o.DataBags["secrets"] = map[string]json.RawMessage{"db": raw(map[string]any{"id": "db", "password": "hunter2"})}
	o.DataBags["empty"] = map[string]json.RawMessage{}
	o.Groups["ops"] = &Group{Users: []string{"alice"}, Clients: []string{"web-01"}, Groups: []string{"admins"}}
	o.ACLs["nodes/web-01"] = map[string]Perm{
		"create": {Actors: []string{"pivotal", "web-01"}, Groups: []string{"admins", "ops"}},
		"read":   {Actors: []string{"pivotal", "web-01"}, Groups: []string{"admins", "ops"}},
		"update": {Actors: []string{"pivotal", "web-01"}, Groups: []string{"admins"}},
		"delete": {Actors: []string{"pivotal"}, Groups: []string{"admins"}},
		"grant":  {Actors: []string{"pivotal"}, Groups: []string{"admins"}},
	}
	s.mu.Unlock()
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
