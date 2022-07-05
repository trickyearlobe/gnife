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
	"testing"
)

func TestReadConfig(t *testing.T) {
	config := ReadConfig("")
	if config.ClientName != "richard" {
		t.Errorf("expected 'richard', got '%s'", config.ClientName)
	}
	if config.ChefServerUrl != "https://chef.thenixons.local/organizations/thenixons" {
		t.Errorf("expected 'https://chef.thenixons.local/organizations/thenixons', got '%s'", config.ChefServerUrl)
	}
}
