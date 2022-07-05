package components

import (
	"fmt"
	"os"
)

func CheckErr(err error) {
	if err == nil {
		return
	} else {
		fmt.Printf("%v\n", err)
		os.Exit(127)
	}
}
