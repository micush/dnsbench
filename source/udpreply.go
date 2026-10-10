package main

import (
	"errors"
	"net"
	"sync"
	"syscall"
)

// udpReplier sends replies on the listening socket without taking Go's per-socket write lock.
//
// net.UDPConn.WriteTo holds a lock for the whole sendto call, so every reply on the port-53 socket is sent one
// at a time.  On a profile from a production node the goroutines spent about 850 goroutine-seconds in 20 s waiting
// for that lock (a reply costs roughly 16 µs of kernel time there, which caps one socket at about 60k replies a
// second no matter how many cores are free).  The kernel itself sends from many threads at once on one UDP
// socket, so the replier calls sendto directly on a duplicate of the socket's descriptor.  The duplicate shares
// the open socket (same port, same buffers, same non-blocking mode), and a send that would block (a full send
// buffer) or an address it cannot express falls back to the ordinary WriteTo, which waits properly.
type udpReplier struct {
	pc net.PacketConn

	mu     sync.RWMutex // read-locked by every send; the write lock is taken once, to close
	fd     int          // -1 once closed, or when there is no duplicate
	family int          // syscall.AF_INET or AF_INET6
}

// newUDPReplier duplicates pc's descriptor.  Anything unexpected gives a replier that just uses pc.WriteTo.
func newUDPReplier(pc net.PacketConn) *udpReplier {
	r := &udpReplier{pc: pc, fd: -1}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		return r
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return r
	}
	_ = rc.Control(func(fd uintptr) {
		sa, err := syscall.Getsockname(int(fd))
		if err != nil {
			return
		}
		switch sa.(type) {
		case *syscall.SockaddrInet4:
			r.family = syscall.AF_INET
		case *syscall.SockaddrInet6:
			r.family = syscall.AF_INET6
		default:
			return
		}
		nfd, err := syscall.Dup(int(fd))
		if err != nil {
			return
		}
		syscall.CloseOnExec(nfd)
		r.fd = nfd
	})
	return r
}

// sockaddr turns a client address into the kernel's form, or nil when it cannot (a zone, a odd type).
func (r *udpReplier) sockaddr(to net.Addr) syscall.Sockaddr {
	ua, ok := to.(*net.UDPAddr)
	if !ok || ua.Zone != "" {
		return nil
	}
	if r.family == syscall.AF_INET {
		ip4 := ua.IP.To4()
		if ip4 == nil {
			return nil
		}
		sa := &syscall.SockaddrInet4{Port: ua.Port}
		copy(sa.Addr[:], ip4)
		return sa
	}
	ip16 := ua.IP.To16()
	if ip16 == nil {
		return nil
	}
	sa := &syscall.SockaddrInet6{Port: ua.Port}
	copy(sa.Addr[:], ip16)
	return sa
}

// WriteTo sends b to the client.
func (r *udpReplier) WriteTo(b []byte, to net.Addr) {
	r.mu.RLock()
	if r.fd >= 0 {
		if sa := r.sockaddr(to); sa != nil {
			for {
				err := syscall.Sendto(r.fd, b, 0, sa)
				if err == nil {
					r.mu.RUnlock()
					return
				}
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				break // EAGAIN (buffer full) or anything else: let the ordinary path decide
			}
		}
	}
	r.mu.RUnlock()
	r.pc.WriteTo(b, to)
}

// close releases the duplicate; it waits for sends in progress, and later sends use pc (which is closed too by then).
func (r *udpReplier) close() {
	r.mu.Lock()
	if r.fd >= 0 {
		syscall.Close(r.fd)
		r.fd = -1
	}
	r.mu.Unlock()
}
