package main

import (
	"encoding/binary"
	"net/netip"
	"os"
	"reflect"
	"testing"
)

// sortMsg builds a response for a.example with a CNAME and the given addresses (compressed owner names).
func sortMsg(addrs ...string) []byte {
	b := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}
	b = append(b, 1, 'a', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 0, 1, 0, 1)
	n := 0
	b = append(b, 0xC0, 12, 0, 5, 0, 1, 0, 0, 0, 60, 0, 2, 0xC0, 12) // CNAME to itself-ish name, pointer into the question
	n++
	for _, s := range addrs {
		a := netip.MustParseAddr(s)
		typ := byte(1)
		if a.Is6() {
			typ = 28
		}
		b = append(b, 0xC0, 12, 0, typ, 0, 1, 0, 0, 0, 60, 0, byte(len(a.AsSlice())))
		b = append(b, a.AsSlice()...)
		n++
	}
	binary.BigEndian.PutUint16(b[6:], uint16(n))
	return b
}

func answerAddrs(t *testing.T, b []byte) []string {
	rrs, _, ok := recordsAfterQuestion(b)
	if !ok {
		t.Fatal("unparsable")
	}
	var out []string
	for _, rr := range rrs {
		if rr.typ == typeA || rr.typ == typeAAAA {
			a, _ := netip.AddrFromSlice(b[rr.rdataOff:rr.end])
			out = append(out, a.String())
		}
	}
	return out
}

func TestSortAnswerPerClientNetwork(t *testing.T) {
	list, err := normalizeSortList([]string{"10.1.0.0/16: 10.0.0.0/8", "any: 192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pool{sorts: buildSortRules(list)}
	msg := sortMsg("8.8.8.8", "10.0.0.5", "10.1.2.3", "192.168.1.1")
	orig := append([]byte(nil), msg...)

	got := answerAddrs(t, p.sortAnswer(msg, netip.MustParseAddr("10.1.9.9")))
	if want := []string{"10.1.2.3", "10.0.0.5", "8.8.8.8", "192.168.1.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("10.1 client: %v, want %v", got, want)
	}
	got = answerAddrs(t, p.sortAnswer(msg, netip.MustParseAddr("172.16.0.1")))
	if want := []string{"192.168.1.1", "8.8.8.8", "10.0.0.5", "10.1.2.3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("any client: %v, want %v", got, want)
	}
	if !reflect.DeepEqual(msg, orig) {
		t.Fatal("the input message was modified")
	}
	// the CNAME stays first and the message still parses
	out := p.sortAnswer(msg, netip.MustParseAddr("10.1.9.9"))
	rrs, _, ok := recordsAfterQuestion(out)
	if !ok || rrs[0].typ != typeCNAME || len(out) != len(msg) {
		t.Fatal("message damaged")
	}
}

func TestSortAnswerLeavesOthersAlone(t *testing.T) {
	p := &Pool{sorts: buildSortRules([]string{"10.0.0.0/8: 10.0.0.0/8"})}
	msg := sortMsg("8.8.8.8", "10.0.0.5")
	if out := p.sortAnswer(msg, netip.MustParseAddr("1.1.1.1")); &out[0] != &msg[0] {
		t.Fatal("a client without a rule got a copy")
	}
	msg[3] |= 3 // NXDOMAIN
	if out := p.sortAnswer(msg, netip.MustParseAddr("10.0.0.1")); &out[0] != &msg[0] {
		t.Fatal("an error answer was touched")
	}
	if got := answerAddrs(t, p.sortAnswer(sortMsg("10.0.0.5", "8.8.8.8"), netip.MustParseAddr("10.0.0.1"))); got[0] != "10.0.0.5" {
		t.Fatalf("already in order: %v", got)
	}
}

func TestSortListValidation(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/8:", "10.0.0.0/8: ", "nonsense: 10.0.0.0/8", "10.0.0.0/8: x"} {
		if _, err := normalizeSortList([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, err := normalizeSortList([]string{"10.1.2.3/16:  10.1.0.0/16 ,192.168.1.5", "2001:db8::/32: 2001:db8:1::/48", "ANY: ::1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.1.0.0/16: 10.1.0.0/16, 192.168.1.5/32", "2001:db8::/32: 2001:db8:1::/48", "any: ::1/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestSortListPlainNetworks(t *testing.T) {
	list, err := normalizeSortList([]string{"10.21.0.0/16", "10.20.0.0/16", "10.22.0.0/16"})
	if err != nil || len(list) != 3 || list[0] != "10.21.0.0/16" {
		t.Fatalf("%v %v", list, err)
	}
	p := &Pool{sorts: buildSortRules(list)}
	msg := sortMsg("10.22.0.9", "8.8.8.8", "10.20.0.7", "10.21.0.3")
	// a client in 10.129/16: its own network first, then the listed order, then the rest
	got := answerAddrs(t, p.sortAnswer(msg, netip.MustParseAddr("10.20.5.5")))
	if want := []string{"10.20.0.7", "10.21.0.3", "10.22.0.9", "8.8.8.8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("10.129 client: %v, want %v", got, want)
	}
	// a client in none of them: the listed order
	got = answerAddrs(t, p.sortAnswer(msg, netip.MustParseAddr("192.168.1.1")))
	if want := []string{"10.21.0.3", "10.20.0.7", "10.22.0.9", "8.8.8.8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outside client: %v, want %v", got, want)
	}
}

func TestSortListCanBeSwitchedOff(t *testing.T) {
	dir := t.TempDir()
	load := func(js string) DNSConfig {
		f := dir + "/c.json"
		if err := os.WriteFile(f, []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
		dc, err := loadConfig(f)
		if err != nil {
			t.Fatal(err)
		}
		return dc.DNS
	}
	// a file that predates the switch keeps its list working
	d := load(`{"dns":{"sortlist":["10.0.0.0/8"]}}`)
	if !d.SortListOn || len(NewPool(d).sorts) == 0 {
		t.Fatal("an existing sort list was switched off")
	}
	d = load(`{"dns":{"sortlist":["10.0.0.0/8"],"sortlist_on":false}}`)
	if d.SortListOn || len(d.SortList) != 1 || len(NewPool(d).sorts) != 0 {
		t.Fatal("off must keep the list and not sort")
	}
}
