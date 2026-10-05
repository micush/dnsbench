package main

// Go's syscall package has no SYS_SENDMMSG for 386.
const (
	sysSendmmsg = 345
	sysRecvmmsg = 337
)
