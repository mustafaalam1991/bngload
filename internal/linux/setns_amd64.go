//go:build linux && amd64

package linux

// sysSetns is the setns(2) system call number on x86-64. The Go standard
// library syscall package does not define it for this architecture.
const sysSetns = 308
