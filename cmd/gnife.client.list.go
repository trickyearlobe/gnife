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
package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/trickyearlobe/gnife/components"
)

var gnifeClientListCmd = &cobra.Command{
	Use:   "list",
	Short: "Get the list of clients",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeClientList()
	},
}

func init() {
	gnifeClientCmd.AddCommand(gnifeClientListCmd)
}

func gnifeClientList() {
	client := components.GetConfiguredChefClient()
	response, err := client.Clients.List()
	components.CheckErr(err)
	output, err := json.MarshalIndent(response, "", "  ")
	components.CheckErr(err)
	fmt.Println(string(output))
}
