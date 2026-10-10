package main

import (
	"strings"
	"testing"
)

func TestGatewayNameValidation(t *testing.T) {
	g := defaultGroup()
	for _, ok := range []string{"", "Office DNS", strings.Repeat("é", maxGatewayName)} {
		g.Name = ok
		if err := g.Validate(); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{strings.Repeat("x", maxGatewayName+1), "a\nb", "tab\there"} {
		g.Name = bad
		if g.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A name is shared across the cluster; an unnamed cluster hashes exactly as before.
func TestGatewayNameIsSharedAndHashStable(t *testing.T) {
	a := newDaemonConfig()
	before := sharedOf(a).hash()
	if strings.Contains(string(mustJSON(t, sharedOf(a))), `"name"`) {
		t.Fatal("empty name must be omitted from the shared document")
	}
	a.Groups[0].Name = "Office DNS"
	if sharedOf(a).hash() == before {
		t.Fatal("renaming did not change the shared hash")
	}
	b := newDaemonConfig()
	if _, err := mergeShared(b, sharedOf(a), seedsOf(b)); err != nil {
		t.Fatal(err)
	}
	if b.Groups[0].Name != "Office DNS" {
		t.Fatalf("replica did not get the name: %q", b.Groups[0].Name)
	}
}

func TestCanvasSetAndClearName(t *testing.T) {
	dc := newDaemonConfig()
	id := dc.Groups[0].GroupID
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: id, Label: "Office DNS"}); err != nil {
		t.Fatal(err)
	}
	if dc.Groups[0].Name != "Office DNS" {
		t.Fatalf("name = %q", dc.Groups[0].Name)
	}
	if cg := buildCanvas(dc, nil, nil); cg[0].Name != "Office DNS" {
		t.Fatalf("canvas name = %q", cg[0].Name)
	}
	gs := []GatewayGroup{{GroupID: id}, {GroupID: 99}}
	nameGateways(gs, dc)
	if gs[0].Name != "Office DNS" || gs[1].Name != "" {
		t.Fatalf("nameGateways: %+v", gs)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "set", Kind: "gateway", Group: id, Label: "-"}); err != nil || dc.Groups[0].Name != "" {
		t.Fatalf("clear: %v %q", err, dc.Groups[0].Name)
	}
	if _, err := applyCanvasEdit(dc, canvasEdit{Action: "add", Kind: "gateway", VIP: "10.9.9.1/24", Label: "Lab"}); err != nil {
		t.Fatal(err)
	}
	if dc.Groups[len(dc.Groups)-1].Name != "Lab" {
		t.Fatal("add did not set the name")
	}
}
