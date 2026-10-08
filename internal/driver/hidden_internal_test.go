package driver

import (
	"bytes"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("a control notify of an unknown op", func() {
	It("names a credential index object it invalidates by its kind", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		params := meta.ZoneParams{
			UserKeysPool:  meta.ParsePool("z.rgw.meta:users.keys"),
			UserEmailPool: meta.ParsePool("z.rgw.meta:users.email"),
			UserSwiftPool: meta.ParsePool("z.rgw.meta:users.swift"),
		}
		c := newObjectCache(3, 0, meta.ParsePool("z.rgw.meta:root"), time.Now)
		c.hidden = newHiddenPools(params)
		c.onNotify(meta.CacheNotifyInfo{Op: 99, Obj: meta.RawObj{Pool: params.UserKeysPool, OID: "AKUNKNOWNOP"}})
		Expect(buf.String()).To(ContainSubstring(`"msg":"invalidating for a control notify of an unknown op"`))
		Expect(buf.String()).To(ContainSubstring(`"kind":"user key index"`))
		Expect(buf.String()).NotTo(ContainSubstring("AKUNKNOWNOP"))
	})
})
