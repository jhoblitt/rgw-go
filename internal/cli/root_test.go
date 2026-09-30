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

var _ = Describe("Argv", func() {
	It("passes rgw-go's own argv through", func() {
		Expect(cli.Argv([]string{"/usr/bin/rgw-go", "version"})).To(Equal([]string{"version"}))
	})
	It("rewrites radosgw's argv into serve -- args", func() {
		Expect(cli.Argv([]string{"/usr/bin/radosgw", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"})).
			To(Equal([]string{"serve", "--", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"}))
	})
	It("matches the base name only", func() {
		Expect(cli.Argv([]string{"radosgw"})).To(Equal([]string{"serve", "--"}))
		Expect(cli.Argv([]string{"/opt/radosgw-wrapper", "x"})).To(Equal([]string{"x"}))
	})
})
