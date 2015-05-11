// Copyright (c) 2015 RightScale, Inc. - see LICENSE

package main

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

func TestWstunnel(t *testing.T) {
	format.UseStringerRepresentation = true
	RegisterFailHandler(Fail)
	RunSpecs(t, "WSTUNNEL")
}
