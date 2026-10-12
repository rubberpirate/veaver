//go:build linux

package console

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func ioctl(fd, request uintptr, value unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if e != 0 {
		return e
	}
	return nil
}

func terminal() (func(), error) {
	var original syscall.Termios
	if e := ioctl(os.Stdin.Fd(), syscall.TCGETS, unsafe.Pointer(&original)); e != nil {
		return nil, fmt.Errorf("TUI needs an interactive terminal; use --once for a snapshot: %w", e)
	}
	var size [4]uint16
	if e := ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); e != nil {
		return nil, e
	}
	state := original
	state.Lflag &^= syscall.ICANON | syscall.ECHO
	state.Iflag &^= syscall.ICRNL | syscall.IXON
	state.Cc[syscall.VMIN] = 0
	state.Cc[syscall.VTIME] = 1
	if e := ioctl(os.Stdin.Fd(), syscall.TCSETS, unsafe.Pointer(&state)); e != nil {
		return nil, e
	}
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")
	return func() {
		_ = ioctl(os.Stdin.Fd(), syscall.TCSETS, unsafe.Pointer(&original))
		fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
	}, nil
}

func dimensions() (int, int) {
	var size [4]uint16
	if ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)) != nil || size[0] == 0 || size[1] == 0 {
		return 100, 30
	}
	return int(size[1]), int(size[0])
}
