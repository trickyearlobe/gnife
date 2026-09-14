package chefserver

import (
	"encoding/json"
	"testing"
)

func doc(t *testing.T, kind, js string) *document {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(js), &obj); err != nil {
		t.Fatal(err)
	}
	return index(kind, obj)
}

func TestSearchQueries(t *testing.T) {
	node := doc(t, "node", `{
		"name": "web-01", "chef_environment": "prod",
		"run_list": ["role[web]", "recipe[nginx]", "recipe[app::api]"],
		"default": {"kernel": {"machine": "x86_64"}, "tier": "default-tier"},
		"normal": {"tags": ["pci", "eu-west"], "tier": "normal-tier"},
		"override": {"tier": "override-tier"},
		"automatic": {"platform": "ubuntu", "platform_version": "22.04", "ipaddress": "10.0.0.5",
		              "cpu": {"total": 8}, "roles": ["web"], "fqdn": "web-01.example.com"}
	}`)
	cases := []struct {
		q    string
		want bool
	}{
		{"*:*", true},
		{"name:web-01", true},
		{"name:web-02", false},
		{"name:web-*", true},
		{"name:web-0?", true},
		{"chef_environment:prod", true},
		{"role:web", true},
		{"role:db", false},
		{"recipe:nginx", true},
		{"recipe:nginx::default", true},
		{"recipe:app::api", true},
		{"run_list:role[web]", true},
		{"platform:ubuntu", true},
		{"platform:ubuntu AND platform_version:22.04", true},
		{"platform:ubuntu platform_version:22.04", true},
		{"platform:ubuntu platform_version:20.04", false},
		{"platform:centos OR platform:ubuntu", true},
		{"NOT platform:ubuntu", false},
		{"-platform:centos", true},
		{"(platform:centos OR platform:ubuntu) AND role:web", true},
		{"(platform:centos OR platform:debian) AND role:web", false},
		{"kernel_machine:x86_64", true},
		{"machine:x86_64", true},
		{"tier:override-tier", true}, // override wins
		{"tier:normal-tier", false},
		{"tags:pci", true},
		{"tags:eu-west", true},
		{"cpu_total:[4 TO 16]", true},
		{"cpu_total:[9 TO 16]", false},
		{"cpu_total:{8 TO 16}", false},
		{"platform_version:[20 TO *]", true},
		{"ipaddress:10.0.0.5", true},
		{"fqdn:\"web-01.example.com\"", true},
		{"web-01", true}, // bare term matches a value
		{"nonsense", false},
		{"chef_environment:prod AND NOT tags:pci", false},
	}
	for _, c := range cases {
		q, err := parseQuery(c.q)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if got := q.match(node); got != c.want {
			t.Errorf("%q: got %v want %v", c.q, got, c.want)
		}
	}
	for _, bad := range []string{"(a:b", "a:[1 TO"} {
		if _, err := parseQuery(bad); err == nil {
			t.Errorf("%q should fail to parse", bad)
		}
	}
}

func TestSearchDataBagAndRole(t *testing.T) {
	item := doc(t, "data_bag_item", `{"name":"data_bag_item_users_alice","raw_data":{"id":"alice","groups":["ops","dev"],"shell":"/bin/zsh"}}`)
	for q, want := range map[string]bool{"id:alice": true, "groups:ops": true, "shell:/bin/bash": false} {
		p, _ := parseQuery(q)
		if p.match(item) != want {
			t.Errorf("%q: want %v", q, want)
		}
	}
	role := doc(t, "role", `{"name":"web","run_list":["recipe[nginx]"],"default_attributes":{"nginx":{"port":80}}}`)
	for q, want := range map[string]bool{"name:web": true, "recipe:nginx": true, "nginx_port:80": true, "port:80": true} {
		p, _ := parseQuery(q)
		if p.match(role) != want {
			t.Errorf("%q: want %v", q, want)
		}
	}
}
