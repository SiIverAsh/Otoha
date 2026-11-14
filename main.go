package main

import (
	"fmt"
	"os"

	"github.com/SiIverAsh/otoha/cmd"
)

func main() {
	if err := cmd.Execute(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "otoha: %v\n", err)
		os.Exit(1)
	}
}
