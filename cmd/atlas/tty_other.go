//go:build !linux

package main

func isTerminal(fd uintptr) bool { return false }

