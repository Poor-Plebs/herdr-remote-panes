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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// Kept before the walk begins, because dir is reassigned all the way to
	// the filesystem root before the search gives up: naming it down there
	// would say "/", the one directory the answer is certainly not in. The
	// comment below has asked for the starting point since this was written
	// and did not get it, for exactly that reason.
	start := dir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// The filesystem root, with no go.mod anywhere above. Worth saying
			// where it started: a test failing here is not about what it tests.
			return "", fmt.Errorf("no go.mod above %s, so the top of the "+
				"repository could not be found", start)
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

// Planned reports a page that describes something not built yet.
//
// docs/pairing.md is a design written down so that decisions already taken are
// not argued again, and it says so at the top. It is documentation of the
// project rather than of the plugin, and that difference matters to everything
// which reads the docs to find out what this does.
func Planned(page []byte) bool {
	return plannedMarker.Match(page)
}

// plannedMarker is held to a shape rather than searched for loosely, so that a
// page using the word in a sentence somewhere is not mistaken for one that is
// entirely about something unbuilt.
var plannedMarker = regexp.MustCompile(`(?mi)^Status: \*\*planned`)

// DocsText is every page from [DocPages] that describes what the plugin does,
// joined together.
//
// Pages about what it does not do yet are left out. Several guards read this to
// ask whether something is explained to somebody who went looking -- a menu
// state, a warning, a setting -- and a page describing a feature that does not
// exist answers none of those. Being satisfied by one is worse than failing:
// the phrase is then documented nowhere a reader can act on.
//
// [DocPages] still names every page, which is what the link checking wants.
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
		if Planned(raw) {
			continue
		}
		all = append(all, raw...)
		all = append(all, '\n')
	}
	if len(all) == 0 {
		return "", errors.New("no documentation was read")
	}
	return string(all), nil
}
