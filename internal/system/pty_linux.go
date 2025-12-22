//go:build linux

package system

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type ptyPair struct {
	master *os.File
	slave  *os.File
}

type winsize struct {
	Rows uint16
	Cols uint16
	X    uint16
	Y    uint16
}

func openPTY(cols, rows int) (ptyPair, error) {
	mfd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ptyPair{}, err
	}
	master := os.NewFile(uintptr(mfd), "/dev/ptmx")

	// Unlock PTY.
	var unlock int32
	if err := ioctl(mfd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		_ = master.Close()
		return ptyPair{}, fmt.Errorf("TIOCSPTLCK: %w", err)
	}

	// Get PTY number.
	var n uint32
	if err := ioctl(mfd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		_ = master.Close()
		return ptyPair{}, fmt.Errorf("TIOCGPTN: %w", err)
	}

	sname := fmt.Sprintf("/dev/pts/%d", n)
	sfd, err := syscall.Open(sname, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return ptyPair{}, err
	}
	slave := os.NewFile(uintptr(sfd), sname)

	// Make the PTY behave closer to a typical interactive terminal (CRLF on output, etc).
	// Some minimal distros default to raw-ish flags which can cause "indentation" after newlines in the web terminal.
	_ = setTermiosSane(slave)

	if cols > 0 && rows > 0 {
		_ = setWinSize(master, cols, rows)
	}

	return ptyPair{master: master, slave: slave}, nil
}

func setWinSize(f *os.File, cols, rows int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(int(f.Fd()), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func setTermiosSane(f *os.File) error {
	var tio syscall.Termios
	if err := ioctl(int(f.Fd()), syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); err != nil {
		return err
	}
	// Output post-processing + NL -> CRNL.
	tio.Oflag |= syscall.OPOST | syscall.ONLCR
	// Map CR to NL on input (common for shells/readline).
	tio.Iflag |= syscall.ICRNL
	return ioctl(int(f.Fd()), syscall.TCSETS, uintptr(unsafe.Pointer(&tio)))
}

func ioctl(fd int, req, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}
