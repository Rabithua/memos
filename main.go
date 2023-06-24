package main

import (
	_ "modernc.org/sqlite"

	"github.com/rabithua/memos/cmd"
)

func main() {
	err := cmd.Execute()
	if err != nil {
		panic(err)
	}
}
