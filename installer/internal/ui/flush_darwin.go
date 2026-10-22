package ui

import "golang.org/x/sys/unix"

// darwin has no TCFLSH; TIOCFLUSH takes the FREAD bit for the input queue.
func flushTTY(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1) }
