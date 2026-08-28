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

// DocPages is every page of documentation in the repository: the README at the
// top and the pages under docs/, in that order.
//
// The set rather than the README alone, because which page carries a sentence
// is a decision that changes. Troubleshooting and the contributor notes were
// both in the README and are both their own pages now, and the tests that hold
// a phrase in the prose to what the code prints are not about where the phrase
// sits.
func DocPages() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		return nil, err
	}
	return append([]string{filepath.Join(root, "README.md")}, pages...), nil
}

// DocsText is every page from [DocPages] joined together.
//
// Held in one place because five packages were each keeping their own copy of
// this, which is the arrangement where one of them quietly stops matching the
// others.
func DocsText() (string, error) {
	pages, err := DocPages()
	if err != nil {
		return "", err
	}
	var all []byte
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			return "", err
		}
		all = append(all, raw...)
		all = append(all, '\n')
	}
	if len(all) == 0 {
		return "", errors.New("no documentation was read")
	}
	return string(all), nil
}
