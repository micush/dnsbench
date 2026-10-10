package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	multicast4  = "224.1.3.7"
	multicast6  = "ff02::1:307"
	dgwPort     = 4729
	dgwVersion  = 2
	packetMagic = 0xD1B0
	hmacLen     = 32

	// Wire layout (big endian), protocol version 2:
	//   2B magic | 1B version | 1B type | 1B group | 1B priority | 1B afn_id |
	//   1B flags | 1B af | 16B sender_ip | 16B virtual_ip | 2B hello_ms |
	//   2B hold_ms | 1B lb_method | 1B weight   = 47 bytes, then 32B HMAC-SHA256.
	headerSize = 47
	packetSize = headerSize + hmacLen

	defaultMaxAFNs = 128
)

// dgwOUI is the vendor prefix of every virtual MAC.
var dgwOUI = [3]byte{0x00, 0x1A, 0x7C}

// AF is the address-family flag carried in the header.
type AF uint8

const (
	afIPv4 AF = 0x01
	afIPv6 AF = 0x02
)

func (a AF) String() string {
	if a == afIPv4 {
		return "v4"
	}
	return "v6"
}

type PktType uint8

const (
	pktHello  PktType = 1
	pktCoup   PktType = 2
	pktResign PktType = 3
	pktAck    PktType = 4
)

type State int

const (
	stateInit    State = 1
	stateListen  State = 2
	stateSpeak   State = 3
	stateStandby State = 4
	stateActive  State = 5 // AGC
	stateForward State = 6 // AFN
)

func (s State) String() string {
	switch s {
	case stateInit:
		return "INIT"
	case stateListen:
		return "LISTEN"
	case stateSpeak:
		return "SPEAK"
	case stateStandby:
		return "STANDBY"
	case stateActive:
		return "ACTIVE"
	case stateForward:
		return "FORWARD"
	}
	return fmt.Sprintf("STATE(%d)", int(s))
}

type LBMethod int

const (
	lbRoundRobin LBMethod = 1
	lbWeighted   LBMethod = 2
	lbHostPinned LBMethod = 3
	lbFailover   LBMethod = 4
)

var lbNames = map[LBMethod]string{
	lbRoundRobin: "roundrobin",
	lbWeighted:   "weighted",
	lbHostPinned: "hostpinned",
	lbFailover:   "failover",
}

func (m LBMethod) String() string { return lbNames[m] }

func parseLB(s string) (LBMethod, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for m, n := range lbNames {
		if n == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("unknown lb_method %q (roundrobin, weighted, hostpinned, failover)", s)
}

// ── Packets ──────────────────────────────────────────────────────────────────

type Packet struct {
	Type      PktType
	GroupID   int
	Priority  int
	AfnID     int
	Flags     uint8
	AF        AF
	SenderIP  string
	VirtualIP string
	HelloMS   int
	HoldMS    int
	LB        int
	Weight    int
	Preempt   bool
}

func ipTo16(s string) ([16]byte, error) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return [16]byte{}, err
	}
	return a.WithZone("").As16(), nil // IPv4 comes out in ::ffff:a.b.c.d form
}

func ip16ToString(b []byte, af AF) string {
	if af == afIPv4 {
		return netip.AddrFrom4([4]byte(b[12:16])).String()
	}
	return netip.AddrFrom16([16]byte(b)).String()
}

// buildPacket encodes and signs one packet.  senderIP is the engine's own
// address for this family; vip is the bare VIP (no prefix length).
func buildPacket(cfg *GroupConfig, ptype PktType, af AF, afnID int, flags uint8, senderIP string) ([]byte, error) {
	if cfg.Preempt {
		flags |= 0x01
	}
	flags |= 0x02 // agc_capable

	vip := cfg.vipFor(af)
	if i := strings.IndexByte(vip, '/'); i >= 0 {
		vip = vip[:i]
	}
	sip, err := ipTo16(senderIP)
	if err != nil {
		return nil, fmt.Errorf("sender ip: %w", err)
	}
	vip16, err := ipTo16(vip)
	if err != nil {
		return nil, fmt.Errorf("vip: %w", err)
	}

	buf := make([]byte, headerSize, packetSize)
	binary.BigEndian.PutUint16(buf[0:], packetMagic)
	buf[2] = dgwVersion
	buf[3] = byte(ptype)
	buf[4] = byte(cfg.GroupID)
	buf[5] = byte(cfg.Priority)
	buf[6] = byte(afnID)
	buf[7] = flags
	buf[8] = byte(af)
	copy(buf[9:25], sip[:])
	copy(buf[25:41], vip16[:])
	binary.BigEndian.PutUint16(buf[41:], uint16(cfg.HelloMS))
	binary.BigEndian.PutUint16(buf[43:], uint16(cfg.HoldMS))
	buf[45] = byte(cfg.LBMethod)
	buf[46] = byte(cfg.Weight)

	mac := hmac.New(sha256.New, cfg.keyBytes())
	mac.Write(buf)
	return mac.Sum(buf), nil
}

var errBadPacket = errors.New("invalid packet")

func parsePacket(data, key []byte) (*Packet, error) {
	if len(data) != packetSize {
		return nil, errBadPacket
	}
	header, sig := data[:headerSize], data[headerSize:]
	mac := hmac.New(sha256.New, key)
	mac.Write(header)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errBadPacket
	}
	if binary.BigEndian.Uint16(header[0:]) != packetMagic || header[2] != dgwVersion {
		return nil, errBadPacket
	}
	pt := PktType(header[3])
	if pt < pktHello || pt > pktAck {
		return nil, errBadPacket
	}
	af := AF(header[8])
	if af != afIPv4 && af != afIPv6 {
		return nil, errBadPacket
	}
	flags := header[7]
	return &Packet{
		Type:      pt,
		GroupID:   int(header[4]),
		Priority:  int(header[5]),
		AfnID:     int(header[6]),
		Flags:     flags,
		AF:        af,
		SenderIP:  ip16ToString(header[9:25], af),
		VirtualIP: ip16ToString(header[25:41], af),
		HelloMS:   int(binary.BigEndian.Uint16(header[41:])),
		HoldMS:    int(binary.BigEndian.Uint16(header[43:])),
		LB:        int(header[45]),
		Weight:    int(header[46]),
		Preempt:   flags&0x01 != 0,
	}, nil
}
