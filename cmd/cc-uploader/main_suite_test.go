package main_test

import (
	"fmt"
	"os"

	"github.com/cloudfoundry-incubator/inigo/fake_cc"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/tedsuo/ifrit"

	"testing"
)

var ccUploaderBinary string
var fakeCC *fake_cc.FakeCC
var fakeCCProcess ifrit.Process

func TestCCUploader(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CC Uploader Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	ccUploaderPath, err := gexec.Build("github.com/cloudfoundry-incubator/cc-uploader/cmd/cc-uploader")
	Expect(err).NotTo(HaveOccurred())
	return []byte(ccUploaderPath)
}, func(ccUploaderPath []byte) {
	fakeCCAddress := fmt.Sprintf("127.0.0.1:%d", 6767+GinkgoParallelNode())
	fakeCC = fake_cc.New(fakeCCAddress)

	ccUploaderBinary = string(ccUploaderPath)
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	gexec.CleanupBuildArtifacts()
})

var _ = BeforeEach(func() {
	fakeCCProcess = ifrit.Envoke(fakeCC)
})

var _ = AfterEach(func() {
	fakeCCProcess.Signal(os.Kill)
	Eventually(fakeCCProcess.Wait()).Should(Receive(BeNil()))
})
