//go:build !linux

package system

import (
	"errors"
	"os"
)

type ptyPair struct {
	master *os.File
	slave  *os.File
}

func openPTY(cols, rows int) (ptyPair, error) {
	return ptyPair{}, errors.New("pty is only supported on linux")
}

func setWinSize(f *os.File, cols, rows int) error {
	return errors.New("pty is only supported on linux")
}
