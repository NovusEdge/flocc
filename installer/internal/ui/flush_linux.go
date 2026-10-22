package ui

import "golang.org/x/sys/unix"

func flushTTY(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
