package main

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func swapUpdLog(t *testing.T) *updateLog {
	old := updlog
	updlog = &updateLog{}
	t.Cleanup(func() { updlog = old })
	return updlog
}

func rrClass(owner string, typ, class uint16, rdata []byte) []byte {
	b := wireName(owner)
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, class)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
	return append(b, rdata...)
}

func updateWith(prereq int, recs ...[]byte) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[2:], opcodeUpdate<<11)
	binary.BigEndian.PutUint16(b[4:], 1)
	binary.BigEndian.PutUint16(b[6:], uint16(prereq))
	binary.BigEndian.PutUint16(b[8:], uint16(len(recs)-prereq))
	b = append(b, wireName("corp.test")...)
	b = binary.BigEndian.AppendUint16(b, typeSOA)
	b = binary.BigEndian.AppendUint16(b, 1)
	for _, r := range recs {
		b = append(b, r...)
	}
	return b
}

func TestUpdateChangesDescribed(t *testing.T) {
	msg := updateWith(1,
		rrClass("taken.corp.test", 255, 255, nil), // prerequisite: name in use; not a change
		rrClass("host1.corp.test", typeA, 1, []byte{192, 0, 2, 77}),
		rrClass("v6.corp.test", typeAAAA, 1, []byte{0x20, 0x01, 0xd, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}),
		rrClass("alias.corp.test", 5, 1, wireName("host1.corp.test")),
		rrClass("old.corp.test", typeA, 255, nil),                  // delete the RRset
		rrClass("gone.corp.test", 255, 255, nil),                   // delete everything at the name
		rrClass("one.corp.test", typeA, 254, []byte{192, 0, 2, 5}), // delete one record
		rrClass("txt.corp.test", 16, 1, []byte{3, 'a', 'b', 'c'}),  // type without a text form
	)
	got, more := updateChanges(msg)
	want := []string{"add host1.corp.test A 192.0.2.77", "add v6.corp.test AAAA 2001:db8::1", "add alias.corp.test CNAME host1.corp.test",
		"delete old.corp.test A", "delete everything at gone.corp.test", "delete one.corp.test A 192.0.2.5", "add txt.corp.test TXT"}
	if !reflect.DeepEqual(got, want) || more != 0 {
		t.Fatalf("got %q more %d", got, more)
	}

	var many [][]byte
	for i := 0; i < updLogChanges+5; i++ {
		many = append(many, rrClass("h"+strconv.Itoa(i)+".corp.test", typeA, 1, []byte{10, 0, 0, byte(i)}))
	}
	got, more = updateChanges(updateWith(0, many...))
	if len(got) != updLogChanges || more != 5 {
		t.Fatalf("cap: %d listed, %d more", len(got), more)
	}

	// a message cut short lists what could be read, and never panics
	full := updateWith(0, many[:3]...)
	for i := 0; i < len(full); i++ {
		if c, _ := updateChanges(full[:i]); c == nil {
			t.Fatalf("nil list at %d", i)
		}
	}
	if c, _ := updateChanges(nil); c == nil || len(c) != 0 {
		t.Fatal("empty message")
	}
}

func TestUpdateLogRingAndRestore(t *testing.T) {
	l := swapUpdLog(t)
	for i := 0; i < updLogMax+50; i++ {
		l.add(UpdateLog{At: time.Now().Unix(), Client: "c" + strconv.Itoa(i), Zone: "z", Result: "NOERROR", OK: true})
	}
	r := l.recent()
	if len(r) != updLogMax || r[0].Client != "c"+strconv.Itoa(updLogMax+49) || r[len(r)-1].Client != "c50" || r[0].Changes == nil {
		t.Fatalf("recent: %d, first %s last %s", len(r), r[0].Client, r[len(r)-1].Client)
	}
	snap := l.snapshot()
	if snap[0].Client != "c50" || len(snap) != updLogMax {
		t.Fatal("snapshot must be oldest first")
	}
	l2 := &updateLog{}
	n := l2.restore(append([]UpdateLog{{At: 0, Client: "bad"}, {At: time.Now().Unix() + 3600, Client: "future"}}, snap...))
	if n != updLogMax || l2.recent()[0].Client != r[0].Client {
		t.Fatalf("restore kept %d", n)
	}
}

func TestUpdateIsLogged(t *testing.T) {
	l := swapUpdLog(t)
	fe, _, pr, _, _ := updRig(t)
	fe.handleUpdate(updateMsg(0x3001, "corp.test", typeSOA, 1), false, mustAddr("::ffff:192.0.2.9"))
	fe.handleUpdate(updateMsg(0x3002, "nosuch.test", typeSOA, 1), true, mustAddr("192.0.2.10"))
	r := l.recent()
	if len(r) != 2 {
		t.Fatalf("%d entries", len(r))
	}
	bad, good := r[0], r[1]
	if !good.OK || good.Result != "NOERROR" || good.Client != "192.0.2.9" || good.Zone != "corp.test" || good.Primary == "" || good.Addr == "" || good.Note != "" ||
		len(good.Changes) != 1 || good.Changes[0] != "add host.corp.test A 192.0.2.7" || good.At == 0 || good.TCP || pr.n() != 1 {
		t.Fatalf("delivered update: %+v", good)
	}
	if bad.OK || bad.Result != "NOTAUTH" || bad.Addr != "" || bad.Note == "" || !bad.TCP || bad.Zone != "nosuch.test" {
		t.Fatalf("refused update: %+v", bad)
	}
}

func TestUpdateLogPersists(t *testing.T) {
	l := swapUpdLog(t)
	l.add(UpdateLog{At: time.Now().Unix(), Client: "192.0.2.9", Zone: "corp.test", Primary: "ns1.corp.test", Addr: "10.0.0.1", Result: "NOERROR", OK: true, Changes: []string{"add a.corp.test A 192.0.2.1"}, More: 2})
	l.add(UpdateLog{At: time.Now().Unix(), Client: "192.0.2.10", Zone: "x.test", Result: "NOTAUTH", Note: "no SOA", TCP: true})
	q, _ := qsTestStats(time.Now())
	h, _, _ := fakeHost(time.Now())
	swapStats(t, q, h)
	dir := t.TempDir()
	if err := savePersisted(dir); err != nil {
		t.Fatal(err)
	}
	want := l.recent()
	l2 := swapUpdLog(t)
	if _, err := loadPersisted(dir); err != nil {
		t.Fatal(err)
	}
	if got := l2.recent(); !reflect.DeepEqual(got, want) || len(got) != 2 {
		t.Fatalf("after a restart:\n got %+v\nwant %+v", got, want)
	}
}
