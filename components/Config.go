/*
Copyright © 2022 Richard Nixon <richard.nixon@btinternet.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package components

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Represents a single Chef Config from ~/.chef/credentials.toml
type Config struct {
	ChefServerUrl string `toml:"chef_server_url,omitempty"`
	ClientName    string `toml:"client_name,omitempty"`
	ClientKey     string `toml:"client_key,omitempty"`
	CookbookPath  string `toml:"cookbook_path,omitempty"`
	SslNoVerify   bool   `toml:"ssl_no_verify,omitempty"`
}

// Represents a credentials file full of Configs
type Configs map[string]Config

// Read the config for the specified profile. If profile is left blank
// then the currently selected profile is used
func ReadConfig(profile string) Config {
	// If a profile is not specified, use the stored context
	if profile == "" {
		profile = chefContext()
	}
	tomlRaw, err := os.ReadFile(chefCredentialsFile())
	CheckErr(err)

	// Parse the TOML content into multiple `configs`
	var configs Configs
	_, err = toml.Decode(string(tomlRaw), &configs)
	CheckErr(err)
	config := configs[profile]

	// Fix up file paths
	config.ClientKey = expandPath(config.ClientKey)
	config.CookbookPath = expandPath(config.CookbookPath)

	// Fix up Chef server URL if it doesnt end in a /
	if !strings.HasSuffix(config.ChefServerUrl, "/") {
		config.ChefServerUrl = config.ChefServerUrl + "/"
	}

	// Return the selected config
	return config
}

// Returns the path to the users `.chef` directory
func chefConfigDir() string {
	return path.Join(homeDir(), ".chef")
}

// Returns the path to the users `credentials` file
func chefCredentialsFile() string {
	return path.Join(chefConfigDir(), "credentials")
}

// Returns the path to the users `context` file
func chefContextFile() string {
	return path.Join(chefConfigDir(), "context")
}

// Returns current profile from the users `context` file
func chefContext() string {
	text, err := os.ReadFile(chefContextFile())
	CheckErr(err)
	return strings.TrimSpace(string(text))
}

// Convenience wrapper for os.UserHomeDir
func homeDir() string {
	homedir, err := os.UserHomeDir()
	CheckErr(err)
	return homedir
}

// File path expansion for things like ~/
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(homeDir(), path[2:])
	}
	return path
}
