package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		writeError(err)
		os.Exit(1)
	}
	dir := filepath.Dir(exe)
	node := os.Getenv("WINDOWS_BROWSER_BRIDGE_NODE")
	if node == "" {
		node = filepath.Join(dir, "node.exe")
		if _, err := os.Stat(node); err != nil {
			node = "node.exe"
		}
	}
	cli := os.Getenv("WINDOWS_BROWSER_BRIDGE_CLI")
	if cli == "" {
		cli = filepath.Join(dir, "ego-browser.mjs")
	}
	args := append([]string{cli}, os.Args[1:]...)
	command := exec.Command(node, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		writeError(err)
		os.Exit(1)
	}
}

func writeError(err error) {
	_, _ = io.WriteString(os.Stderr, "ego-browser launcher: "+err.Error()+"\n")
}
