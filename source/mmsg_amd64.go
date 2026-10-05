package main

// Go's syscall package has no SYS_SENDMMSG for amd64.
const (
	sysSendmmsg = 307
	sysRecvmmsg = 299
)
