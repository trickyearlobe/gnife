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
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/trickyearlobe/gnife/components"
)

var gnifeRawCmd = &cobra.Command{
	Use:   "raw",
	Short: "Perform raw API operations",
}

var gnifeRawGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Perform a raw API GET",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeRawGet()
	},
}

var gnifeRawPostCmd = &cobra.Command{
	Use:   "post",
	Short: "Perform a raw API POST",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeRawPost()
	},
}

var gnifeRawPutCmd = &cobra.Command{
	Use:   "put",
	Short: "Perform a raw API PUT",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeRawPut()
	},
}

var gnifeRawDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Perform a raw API DELETE",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeRawDelete()
	},
}

func gnifeRawGet() {
	gnifeRawOperation("GET")
}

func gnifeRawPut() {
	gnifeRawOperation("PUT")
}
func gnifeRawPost() {
	gnifeRawOperation("POST")
}
func gnifeRawDelete() {
	gnifeRawOperation("DELETE")
}

func gnifeRawOperation(method string) {
	client := components.GetConfiguredChefClient()
	url, _ := gnifeRawCmd.PersistentFlags().GetString("endpoint")
	data, _ := gnifeRawCmd.PersistentFlags().GetString("data")
	url = strings.TrimPrefix(url, "/")
	request, err := client.NewRequest(method, url, strings.NewReader(data))
	components.CheckErr(err)
	response, err := client.Do(request, nil)
	components.CheckErr(err)
	defer response.Body.Close()
	rawbody, err := io.ReadAll(response.Body)
	components.CheckErr(err)
	body := make(map[string]interface{})
	err = json.Unmarshal(rawbody, &body)
	components.CheckErr(err)
	output, err := json.MarshalIndent(body, "", "  ")
	components.CheckErr(err)
	fmt.Println(string(output))
}

func init() {
	gnifeCmd.AddCommand(gnifeRawCmd)
	gnifeRawCmd.AddCommand(gnifeRawGetCmd)
	gnifeRawCmd.AddCommand(gnifeRawPostCmd)
	gnifeRawCmd.AddCommand(gnifeRawPutCmd)
	gnifeRawCmd.AddCommand(gnifeRawDeleteCmd)
	gnifeRawCmd.PersistentFlags().StringP("endpoint", "e", "/", "the API endpoint")
	gnifeRawCmd.PersistentFlags().StringP("data", "d", "", "the data to be passed to the API endpoint")
}
