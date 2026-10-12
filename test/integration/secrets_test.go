package integration_test

import (
	"fmt"

	goadmin "github.com/ceph/go-ceph/rgw/admin"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

// masked stands in for a secret key that is set.
const masked = "<redacted>"

// A failed assertion prints the values it compared into the job log, which is
// public, so every secret key of go-ceph's admin key types that Gomega would
// print is masked, at any depth: the gateways generate them for the users the
// specs create. An unset secret prints as empty, so a missing one still shows.
var _ = format.RegisterCustomFormatter(maskSecrets)

func maskSecrets(value any) (string, bool) {
	switch v := value.(type) {
	case goadmin.UserKeySpec:
		v.SecretKey = mask(v.SecretKey)
		return fmt.Sprintf("%T%+v", v, v), true
	case goadmin.SwiftKeySpec:
		v.SecretKey = mask(v.SecretKey)
		return fmt.Sprintf("%T%+v", v, v), true
	case goadmin.SubuserSpec:
		return fmt.Sprintf("%T{Name:%s Access:%s SecretKey:%s Secret:%s}",
			v, v.Name, v.Access, maskPtr(v.SecretKey), maskPtr(v.Secret)), true
	}
	return "", false
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return masked
}

func maskPtr(s *string) string {
	if s == nil {
		return "nil"
	}
	return mask(*s)
}

// generated is a secret key as a gateway generates one, for maskSecrets' specs.
const generated = "GeneratedSecret0000000000000000000000001"

// generatedKeys is a user's key list as CreateUser answers it.
func generatedKeys() []goadmin.UserKeySpec {
	return []goadmin.UserKeySpec{{User: "rook-user", AccessKey: "AKEY", SecretKey: generated}}
}

var _ = Describe("maskSecrets", func() {
	It("keeps a generated secret out of HaveLen's failure message on a user's keys", func() {
		keys := generatedKeys()
		m := HaveLen(2)
		Expect(m.Match(keys)).To(BeFalse())
		msg := m.FailureMessage(keys)
		Expect(msg).To(ContainSubstring("rook-user"))
		Expect(msg).To(ContainSubstring(masked))
		Expect(msg).NotTo(ContainSubstring(generated))
	})

	It("keeps a generated secret out of ConsistOf's failure message", func() {
		keys := generatedKeys()
		m := ConsistOf(HaveField("AccessKey", "OTHER"))
		Expect(m.Match(keys)).To(BeFalse())
		Expect(m.FailureMessage(keys)).NotTo(ContainSubstring(generated))
	})

	It("masks the Swift and subuser key types, nested in a struct", func() {
		s := generated
		out := format.Object(struct {
			Swift   []goadmin.SwiftKeySpec
			Subuser goadmin.SubuserSpec
		}{
			Swift:   []goadmin.SwiftKeySpec{{User: "rook-user:swift", SecretKey: generated}},
			Subuser: goadmin.SubuserSpec{Name: "swift", SecretKey: &s, Secret: &s},
		}, 0)
		Expect(out).To(ContainSubstring("rook-user:swift"))
		Expect(out).NotTo(ContainSubstring(generated))
	})

	It("prints an unset secret as empty", func() {
		Expect(format.Object(goadmin.UserKeySpec{AccessKey: "AKEY"}, 0)).NotTo(ContainSubstring(masked))
	})
})
