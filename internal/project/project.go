// Package project holds the checks that are about this repository rather than
// about any one package in it: that every doc comment names its own function,
// that the links between the docs point at things that exist, that the build
// script says something useful on a machine without Go, and that one build of
// the daemon hands the control socket over to the next.
//
// They live together because none of them belongs to a package -- they are
// about the tree -- and because they all need to find the top of it, which is
// what [Root] is for.
package project

import (
	"errors"
	"os"
	"path/filepath"
)

// Root is the top of the repository: the directory holding go.mod.
//
// A test runs in the directory of the package it is in, and these tests are
// about files that live at the top -- README.md, build.sh, the whole tree of
// source. Walking up to go.mod finds it from wherever the package sits, so
// moving a test to another directory does not mean counting ".." again in
// every path it opens.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// The filesystem root, with no go.mod anywhere above. Worth saying
			// where it started: a test failing here is not about what it tests.
			return "", errors.New("no go.mod above the working directory, so the " +
				"top of the repository could not be found")
		}
		dir = parent
	}
}
