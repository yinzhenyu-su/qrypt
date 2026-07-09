package main

import (
	"os"

	"github.com/yinzhenyu/qrypt/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
