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
	"fmt"

	"github.com/spf13/cobra"
)

// These variables should be injected by the build system
// using `go build -ldflags -X main.Version="some version" -X main.GitCommit="some commit" -X main.BuiltByName="some name"
// main.go sets them here before it initialises Cobra
var Build string
var GitCommit string
var GitBranch string
var BuiltByName string

var gnifeVersionCmd = &cobra.Command{
	Use:   "version",
	Short: "show build and version info",
	Run: func(cmd *cobra.Command, args []string) {
		gnifeVersionShow()
	},
}

func gnifeVersionShow() {
	fmt.Printf("Build:      %v\n", Build)
	fmt.Printf("Git commit: %v\n", GitCommit)
	fmt.Printf("Git branch: %v\n", GitBranch)
	fmt.Printf("Built By:   %v\n", BuiltByName)
}

func init() {
	gnifeCmd.AddCommand(gnifeVersionCmd)
}
