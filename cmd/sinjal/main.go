package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches subcommands and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		return fatalf(stderr, "serve is not implemented yet")
	case "version":
		fmt.Fprintf(stdout, "sinjal %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "sinjal: unknown command %q\n\nusage: sinjal [serve|version]\n", cmd)
		return 2
	}
}

// fatalf reports an unrecoverable startup error and returns exit code 1.
func fatalf(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "sinjal: "+format+"\n", a...)
	return 1
}
