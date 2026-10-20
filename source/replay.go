package main

import "time"

// Replay protection for the gateway protocol (wire version 3).  Every packet carries the sender's clock in Unix
// milliseconds under the signature.  A packet is accepted only when
//   - its timestamp is within packetMaxSkew of this node's clock, so a recording made earlier than that is useless, and
//   - it is newer than every packet already taken from the same sender, so a recording cannot be played again inside
//     that time either, and one that was held back and delivered late is refused once a later one has arrived.
//
// The newest timestamp of each sender is kept in memory; a node that has just started has none and takes the first
// packet from each sender that is within the skew limit.  Datagrams that arrive out of order are dropped like any
// lost datagram: the protocol repeats its hellos, so a drop costs nothing.

const (
	replayTableMax = 4096            // senders remembered; beyond this the ones outside the skew window are forgotten
	skewWarnEvery  = 1 * time.Minute // a sender whose clock is off is named in the log at most this often
)

// replayLocked reports whether pkt (already authenticated) may be acted on, and remembers its timestamp when so.
// e.mu is held.
func (e *Engine) replayLocked(pkt *Packet) bool {
	now := wireNow()
	stamp := time.UnixMilli(pkt.Stamp)
	if d := now.Sub(stamp); d > packetMaxSkew || d < -packetMaxSkew {
		if e.skewWarned == nil {
			e.skewWarned = map[string]time.Time{}
		}
		if t, ok := e.skewWarned[pkt.SenderIP]; !ok || now.Sub(t) > skewWarnEvery {
			if len(e.skewWarned) > 256 {
				e.skewWarned = map[string]time.Time{}
			}
			e.skewWarned[pkt.SenderIP] = now
			warnf("Packet from %s is %.0f s off this node's clock; it is dropped (check the time on both nodes, %s)",
				pkt.SenderIP, d.Seconds(), e.tag())
		}
		return false
	}
	if e.stamps == nil {
		e.stamps = map[string]int64{}
	}
	if last, ok := e.stamps[pkt.SenderIP]; ok && pkt.Stamp <= last {
		debugf("Dropped a packet from %s that is not newer than the last one (a replay or reordered, %s)", pkt.SenderIP, e.tag())
		return false
	}
	if len(e.stamps) >= replayTableMax {
		cut := now.Add(-packetMaxSkew).UnixMilli()
		for ip, st := range e.stamps {
			if st < cut {
				delete(e.stamps, ip)
			}
		}
		if len(e.stamps) >= replayTableMax { // all of them recent: this many live senders is not a gateway, so start over
			e.stamps = map[string]int64{}
		}
	}
	e.stamps[pkt.SenderIP] = pkt.Stamp
	return true
}
