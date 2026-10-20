package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// The list of recent dynamic DNS updates (Monitor ▸ Statistics, "Recent dynamic updates"; --dns-updates).
// Every update the proxy handled is one entry: when, who sent it, the zone, what it changed, which primary
// it went to and how that answered.  The last updLogMax are kept; persist.go saves them with the statistics,
// so a restart keeps them.

const (
	updLogMax     = 200
	updLogChanges = 8 // records listed per entry; the rest are counted in More
)

// UpdateLog is one handled update.
type UpdateLog struct {
	At      int64    `json:"at"`
	Client  string   `json:"client"`
	Zone    string   `json:"zone"`
	Primary string   `json:"primary,omitempty"` // the server named in the zone's SOA
	Addr    string   `json:"addr,omitempty"`    // the address the message went to
	Result  string   `json:"result"`            // NOERROR, REFUSED, NOTAUTH, ...
	OK      bool     `json:"ok"`
	Note    string   `json:"note,omitempty"` // why it was not delivered
	TCP     bool     `json:"tcp,omitempty"`
	Changes []string `json:"changes"`        // "add host1.example.com A 192.0.2.7", "delete old.example.com A", ...
	More    int      `json:"more,omitempty"` // records in the message beyond the listed ones
}

type updateLog struct {
	mu   sync.Mutex
	list []UpdateLog // oldest first
}

// updlog is the daemon-wide list.
var updlog = &updateLog{}

func (l *updateLog) add(u UpdateLog) {
	if u.Changes == nil {
		u.Changes = []string{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.list) >= updLogMax {
		l.list = append(l.list[:0], l.list[len(l.list)-updLogMax+1:]...)
	}
	l.list = append(l.list, u)
}

// recent returns the entries, newest first.
func (l *updateLog) recent() []UpdateLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]UpdateLog, 0, len(l.list))
	for i := len(l.list) - 1; i >= 0; i-- {
		out = append(out, l.list[i])
	}
	return out
}

// snapshot returns a copy, oldest first (the order it is saved in).
func (l *updateLog) snapshot() []UpdateLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]UpdateLog(nil), l.list...)
}

// restore replaces the list with a saved one.
func (l *updateLog) restore(list []UpdateLog) int {
	if len(list) > updLogMax {
		list = list[len(list)-updLogMax:]
	}
	now := time.Now().Unix()
	keep := make([]UpdateLog, 0, len(list))
	for _, u := range list {
		if u.At <= 0 || u.At > now+60 {
			continue
		}
		if u.Changes == nil {
			u.Changes = []string{}
		}
		keep = append(keep, u)
	}
	l.mu.Lock()
	l.list = keep
	l.mu.Unlock()
	return len(keep)
}

// updateChanges describes what an UPDATE message asks for: the records of its update section, in the words
// of RFC 2136 section 2.5.  At most updLogChanges are listed; more says how many were left out.  A message
// that cannot be read to the end lists what could be read.
func updateChanges(msg []byte) (changes []string, more int) {
	changes = []string{}
	if len(msg) < 12 {
		return
	}
	pr, up := int(binary.BigEndian.Uint16(msg[6:])), int(binary.BigEndian.Uint16(msg[8:]))
	off := 12
	_, n, err := readName(msg, off) // the zone
	if err != nil || n+4 > len(msg) {
		return
	}
	off = n + 4
	for i := 0; i < pr+up; i++ {
		name, n, err := readName(msg, off)
		if err != nil || n+10 > len(msg) {
			return
		}
		typ, class := binary.BigEndian.Uint16(msg[n:]), binary.BigEndian.Uint16(msg[n+2:])
		rdlen := int(binary.BigEndian.Uint16(msg[n+8:]))
		rd := n + 10
		if rd+rdlen > len(msg) {
			return
		}
		off = rd + rdlen
		if i < pr { // prerequisites are not changes
			continue
		}
		if len(changes) >= updLogChanges {
			more++
			continue
		}
		changes = append(changes, describeChange(msg, name, typ, class, rd, rdlen))
	}
	return
}

func describeChange(msg []byte, name string, typ, class uint16, rd, rdlen int) string {
	tn := qtypeName(typ)
	switch class {
	case 255: // ANY: delete an RRset, or every RRset of the name
		if typ == 255 {
			return "delete everything at " + name
		}
		return "delete " + name + " " + tn
	case 254: // NONE: delete one record
		if v := rdataText(msg, typ, rd, rdlen); v != "" {
			return "delete " + name + " " + tn + " " + v
		}
		return "delete " + name + " " + tn
	}
	s := "add " + name + " " + tn
	if v := rdataText(msg, typ, rd, rdlen); v != "" {
		s += " " + v
	}
	return s
}

// rdataText shows the data of the types people update most; other types show nothing.
func rdataText(msg []byte, typ uint16, rd, rdlen int) string {
	switch {
	case typ == typeA && rdlen == 4, typ == typeAAAA && rdlen == 16:
		if a, ok := netip.AddrFromSlice(msg[rd : rd+rdlen]); ok {
			return a.String()
		}
	case typ == 5 || typ == 12 || typ == 2: // CNAME, PTR, NS
		if n, _, err := readName(msg, rd); err == nil {
			return n
		}
	}
	return ""
}

func (u UpdateLog) String() string {
	return fmt.Sprintf("%s %s %s %s", time.Unix(u.At, 0).Format("2006-01-02 15:04:05"), u.Client, u.Zone, u.Result)
}
