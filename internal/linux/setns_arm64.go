//go:build linux && arm64

package linux

// sysSetns is the setns(2) system call number on arm64.
const sysSetns = 268
