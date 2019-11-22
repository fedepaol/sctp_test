package test_test

import (
	"flag"
	"testing"

	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/reporters"
	. "github.com/onsi/gomega"
)

func TestTest(t *testing.T) {
	RegisterFailHandler(Fail)

	junitPath := flag.String("junit", "junit.xml", "the path for the junit format report")
	flag.Parse()

	rr := []Reporter{}
	if junitPath != nil {
		rr = append(rr, reporters.NewJUnitReporter(*junitPath))
	}
	RunSpecsWithDefaultAndCustomReporters(t, "Foo Suite", rr)
	RunSpecs(t, "Test Suite")
}
