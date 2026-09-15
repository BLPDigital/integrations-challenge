//go:build !hidden

package main

import "os"

// internalSubcommands is empty in a grader built without the `hidden` tag.
//
// The mutation-testing runner is the only internal subcommand, and it names
// every deliberate defect the grader is checked against, which is a list of
// every trap in the artifact. This file exists so the shipped binary answers
// "unknown subcommand" for it and the shipped source does not name it at all.
var internalSubcommands = map[string]func([]string, *os.File, *os.File) error{}

// internalUsage returns the usage lines of the internal subcommands: none here.
func internalUsage() []string { return nil }
