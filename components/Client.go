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

	"github.com/go-chef/chef"
)

func GetConfiguredChefClient() *chef.Client {
	config := ReadConfig("")
	key, err := os.ReadFile(config.ClientKey)
	CheckErr(err)
	client, err := chef.NewClient(&chef.Config{
		Name:    config.ClientName,
		Key:     string(key),
		BaseURL: config.ChefServerUrl,
	})
	CheckErr(err)
	return client
}
