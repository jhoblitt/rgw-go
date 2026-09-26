package cli_test

import (
	"bytes"
	"context"
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/version"
)

var _ = Describe("Run", func() {
	It("prints the version", func() {
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{"version"}, strings.NewReader(""), &out, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(Equal(version.String()+"\n"), "version output")
	})
	It("refuses to serve in phase 0", func() {
		err := cli.Run(context.Background(), []string{"serve"}, strings.NewReader(""), io.Discard, io.Discard)
		Expect(err).To(MatchError(ContainSubstring("not implemented")))
	})
})
