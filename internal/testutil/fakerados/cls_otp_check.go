package fakerados

import "time"

// otpCheckMethods are the methods OTPClassAt emulates of those that check a
// device's code, otp_check and otp_get_result, which the class registers
// CLS_METHOD_RD|CLS_METHOD_WR and CLS_METHOD_RD
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L564-L569 at v19.2.7 and v20.2.4),
// on the class's clock now. The table is empty, so a call to either is
// EOPNOTSUPP.
func otpCheckMethods(func() time.Time) methodTable { return methodTable{} }
