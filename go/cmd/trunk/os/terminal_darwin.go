package os

import "golang.org/x/sys/unix"

const getTermios = unix.TIOCGETA
const setTermios = unix.TIOCSETA
