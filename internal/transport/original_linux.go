//go:build linux

package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// SO_ORIGINAL_DST returns the pre-REDIRECT IPv4 destination from conntrack.
func OriginalDestination(c *net.TCPConn) (string, error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return "", e
	}
	var addr [16]byte
	size := uint32(len(addr))
	var callErr error
	e = raw.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.SOL_IP, 80, uintptr(unsafe.Pointer(&addr[0])), uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			callErr = errno
		}
	})
	if e != nil {
		return "", e
	}
	if callErr != nil {
		return "", callErr
	}
	if size != 16 {
		return "", errors.New("unexpected original destination size")
	}
	return DecodeDestination(addr[:])
}
func DecodeDestination(b []byte) (string, error) {
	if len(b) != 16 || binary.NativeEndian.Uint16(b[:2]) != syscall.AF_INET {
		return "", errors.New("invalid IPv4 sockaddr")
	}
	port := binary.BigEndian.Uint16(b[2:4])
	if port == 0 {
		return "", errors.New("zero destination port")
	}
	return net.JoinHostPort(net.IP(b[4:8]).String(), fmt.Sprint(port)), nil
}
