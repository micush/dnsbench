//go:build linux && !amd64 && !386

package main

import "syscall"

const (
	sysSendmmsg = syscall.SYS_SENDMMSG
	sysRecvmmsg = syscall.SYS_RECVMMSG
)
