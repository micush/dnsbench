package main

import (
	"syscall"
	"unsafe"
)

// mmsghdr mirrors the kernel's struct mmsghdr. Go's natural struct layout
// matches the C layout on every Linux architecture we build for.
type mmsghdr struct {
	hdr syscall.Msghdr
	n   uint32
}

// sendmmsg sends up to len(msgs) datagrams on fd with one system call.
// It returns how many were sent.
func sendmmsg(fd uintptr, msgs []mmsghdr) (int, syscall.Errno) {
	if len(msgs) == 0 {
		return 0, 0
	}
	r, _, e := syscall.Syscall6(sysSendmmsg, fd,
		uintptr(unsafe.Pointer(&msgs[0])), uintptr(len(msgs)), 0, 0, 0)
	if e != 0 {
		return 0, e
	}
	return int(r), 0
}

// recvmmsg receives up to len(msgs) datagrams from fd with one system call.
// flags should include MSG_DONTWAIT for sockets driven by the Go netpoller.
func recvmmsg(fd uintptr, msgs []mmsghdr, flags int) (int, syscall.Errno) {
	if len(msgs) == 0 {
		return 0, 0
	}
	r, _, e := syscall.Syscall6(sysRecvmmsg, fd,
		uintptr(unsafe.Pointer(&msgs[0])), uintptr(len(msgs)), uintptr(flags), 0, 0)
	if e != 0 {
		return 0, e
	}
	return int(r), 0
}
