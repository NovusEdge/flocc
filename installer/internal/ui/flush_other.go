//go:build !linux && !darwin

package ui

func flushTTY(int) {}
