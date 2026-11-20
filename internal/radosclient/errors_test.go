package radosclient_test

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var _ = DescribeTable("ReplyVersion takes for an OSD's reply only the errors one carries",
	func(err error, reply bool) {
		Expect(radosclient.ReplyVersion(err)).To(Equal(reply))
		Expect(radosclient.ReplyVersion(fmt.Errorf("wrapped: %w", err))).To(Equal(reply), "through a wrap")
	},
	Entry("ENOENT", &radosclient.Error{Errno: int32(syscall.ENOENT)}, true),
	Entry("ENOENT as librados reports it, negative", &radosclient.Error{Errno: -int32(syscall.ENOENT)}, true),
	Entry("EEXIST", &radosclient.Error{Errno: int32(syscall.EEXIST)}, true),
	Entry("ECANCELED", &radosclient.Error{Errno: int32(syscall.ECANCELED)}, true),
	Entry("ERANGE", &radosclient.Error{Errno: int32(syscall.ERANGE)}, true),
	Entry("EOVERFLOW", &radosclient.Error{Errno: int32(syscall.EOVERFLOW)}, true),
	Entry("ETIMEDOUT", &radosclient.Error{Errno: int32(syscall.ETIMEDOUT)}, false),
	Entry("ENOTCONN", &radosclient.Error{Errno: int32(syscall.ENOTCONN)}, false),
	Entry("EIO", &radosclient.Error{Errno: int32(syscall.EIO)}, false),
	Entry("a context's", context.DeadlineExceeded, false),
	Entry("a local failure", radosclient.ErrClosed, false),
	Entry("a bare sentinel, no errno", radosclient.ErrNotFound, false),
	Entry("no *Error", errors.New("decoding a result"), false),
)
