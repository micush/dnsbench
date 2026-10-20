package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// rawPeerCall sends a correctly signed (with secret) GET to a node's cluster listener, claiming the identity
// `claim` and presenting the certificate `me` (nil: none).  It returns the status code and the error text.
func rawPeerCall(t *testing.T, secret []byte, to *tnode, claim ClusterPeer, me *tls.Certificate, path string) (int, string) {
	t.Helper()
	pj, _ := json.Marshal(claim)
	peerHdr := base64.StdEncoding.EncodeToString(pj)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randHex(12)
	req, err := http.NewRequest("GET", peerURL(path), bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Ddgw-Peer", peerHdr)
	req.Header.Set("X-Ddgw-Ts", ts)
	req.Header.Set("X-Ddgw-Nonce", nonce)
	req.Header.Set("X-Ddgw-Sig", signMessage(secret, peerHdr, ts, nonce, "GET", path, nil))
	res, err := pinnedClientAs(to.addr, to.mg.cl.node.Fingerprint(), 5*time.Second, me).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(b, &e)
	return res.StatusCode, e.Error
}

func twoNodeCluster(t *testing.T) (a, b *tnode) {
	failDelay = 0
	a, b = newTNode(t, "A"), newTNode(t, "B")
	b.join(a)
	b.sync()
	a.sync()
	return a, b
}

func TestPeerCertMustMatchClaimedIdentity(t *testing.T) {
	a, b := twoNodeCluster(t)
	c := newTNode(t, "C")
	cc, _ := c.mg.cl.node.Identity()
	code, msg := rawPeerCall(t, a.mg.cl.node.Secret(), a, b.mg.cl.node.Self(), &cc, "/cluster/status")
	if code != http.StatusUnauthorized {
		t.Fatalf("a request claiming B but presenting C's certificate: %d %s", code, msg)
	}
	bc, _ := b.mg.cl.node.Identity()
	if code, msg := rawPeerCall(t, a.mg.cl.node.Secret(), a, b.mg.cl.node.Self(), &bc, "/cluster/status"); code != http.StatusOK {
		t.Fatalf("B with its own certificate: %d %s", code, msg)
	}
}

func TestPeerCannotDowngradeToNoCertificate(t *testing.T) {
	a, b := twoNodeCluster(t)
	if !a.mg.cl.node.IsMTLS(b.mg.cl.node.Fingerprint()) {
		t.Fatal("A never noted that B presents its certificate")
	}
	code, msg := rawPeerCall(t, a.mg.cl.node.Secret(), a, b.mg.cl.node.Self(), nil, "/cluster/status")
	if code != http.StatusUnauthorized {
		t.Fatalf("B's identity without a certificate was accepted: %d %s", code, msg)
	}
}

func TestLegacyPeerWithoutCertificateStillWorksUntilStrict(t *testing.T) {
	a, b := twoNodeCluster(t)
	// a member that predates the certificate: nothing recorded about it
	a.mg.cl.node.mu.Lock()
	a.mg.cl.node.st.MTLSFps, a.mg.cl.node.st.StrictOn = nil, false
	a.mg.cl.node.mu.Unlock()
	if a.mg.cl.node.Strict() {
		t.Fatal("strict with a member that was never seen presenting a certificate")
	}
	if code, msg := rawPeerCall(t, a.mg.cl.node.Secret(), a, b.mg.cl.node.Self(), nil, "/cluster/status"); code != http.StatusOK {
		t.Fatalf("a legacy member was refused during a rolling update: %d %s", code, msg)
	}
}

func TestStrictClusterRefusesUnknownSecretHolder(t *testing.T) {
	a, b := twoNodeCluster(t)
	_ = b
	if !a.mg.cl.node.Strict() {
		t.Fatal("expected a strict cluster once its only member presented a certificate")
	}
	r := newTNode(t, "R") // knows the secret (say it was a member once) but is not on A's list
	rc, _ := r.mg.cl.node.Identity()
	code, msg := rawPeerCall(t, a.mg.cl.node.Secret(), a, r.mg.cl.node.Self(), &rc, "/cluster/status")
	if code != http.StatusForbidden {
		t.Fatalf("a non-member with the secret was served: %d %s", code, msg)
	}
	if hasPeer(a, r.addr) {
		t.Fatal("a non-member made itself a member by calling")
	}
}

func TestRemovedNodeStaysRefusedUnderAnotherAddress(t *testing.T) {
	a, b := twoNodeCluster(t)
	secret := b.mg.cl.node.Secret() // the removed node keeps it
	if err := a.mg.cl.RemovePeer(b.addr, "admin"); err != nil {
		t.Fatal(err)
	}
	claim := b.mg.cl.node.Self()
	claim.Addr = "127.0.0.1:1" // a different address, same identity
	bc, _ := b.mg.cl.node.Identity()
	code, _ := rawPeerCall(t, secret, a, claim, &bc, "/cluster/status")
	if code != http.StatusForbidden {
		t.Fatalf("a removed node got in under a new address: %d", code)
	}
	// a fresh identity with the old secret is no member either
	f := newTNode(t, "F")
	fc, _ := f.mg.cl.node.Identity()
	if code, _ := rawPeerCall(t, secret, a, f.mg.cl.node.Self(), &fc, "/cluster/status"); code == http.StatusOK && hasPeer(a, f.addr) {
		t.Fatal("a removed node's secret was enough to join under a new identity")
	}
	// letting the node back in with a join token lifts the block
	b.mg.cl.leaveLocal("test")
	b.join(a)
	if !hasPeer(a, b.addr) {
		t.Fatal("a re-joined node is not a member again")
	}
}
