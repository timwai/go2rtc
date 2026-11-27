package cs2

import (
	"encoding/hex"
	"net/netip"
	"testing"
)

func TestRelayWire(t *testing.T) {
	uid:=encodeUID("ABCDEF-123456-GHIJK")
	if hex.EncodeToString(uid)!="41424344454600000001e2404748494a4b000000" {t.Fatalf("unexpected UID wire format")}
	if encodeUID("ABCDEF-bad-GHIJK")!=nil {t.Fatal("invalid UID accepted")}
	a:=netip.MustParseAddrPort("8.8.4.4:25000")
	if got:=decodeAddr(encodeAddr(a));got!=a {t.Fatalf("address roundtrip: %v",got)}
	if decodeAddr(encodeAddr(netip.MustParseAddrPort("192.168.1.1:25000"))).IsValid() {t.Fatal("private address accepted")}
	if hex.EncodeToString(relayFrame(0x67,[]byte{1,2}))!="f16700020102" {t.Fatal("bad frame")}
}
func TestRelayServerDecode(t *testing.T) {
	key,_:=hex.DecodeString(initKey)
	raw:=[]byte("8.8.8.8,1.1.1.1,192.168.0.1,")
	var enc []byte
	state:=byte(57)
	for i,b:=range raw {
		x:=b^state^key[i%len(key)]
		enc=append(enc,'A'+(x>>4),'A'+(x&15))
		state^=b
	}
	got:=relayServers(string(enc)+":suffix")
	if len(got)!=2 || got[0].String()!="8.8.8.8:32100" || got[1].String()!="1.1.1.1:32100" {t.Fatal("unexpected server list")}
	if relayServers("ZQ")!=nil {t.Fatal("invalid nibble accepted")}
}
