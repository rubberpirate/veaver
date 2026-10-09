//go:build linux

package transport

import (
	"net"
	"syscall"
	"unsafe"
)

// Linux 6.8+ exposes the total subflow count at byte 80 of MPTCP_INFO.
// Older kernels report unknown rather than treating negotiation as bonding.
func subflows(c *net.TCPConn) (int, bool) {
	if c == nil {
		return 0, false
	}
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, false
	}
	var buf [96]byte
	length := uint32(len(buf))
	var errno syscall.Errno
	e = raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, 284, 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&length)), 0)
	})
	if e != nil || errno != 0 {
		return 0, false
	}
	return decodeSubflows(buf[:min(int(length), len(buf))])
}

func decodeSubflows(info []byte) (int, bool) {
	if len(info) < 81 {
		return 0, false
	}
	return int(info[80]), true
}
