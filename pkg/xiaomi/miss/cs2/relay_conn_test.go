package cs2

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestLinkReply(t *testing.T) {
	in:=make([]byte,88)
	in[0],in[1]=0xa1,1
	copy(in[4:],[]byte{1,2,3,4})
	copy(in[40:],encodeAddr(netip.MustParseAddrPort("8.8.8.8:12345")))
	out:=linkReply(in,netip.MustParseAddrPort("192.168.1.2:1234"))
	if len(out)!=88 || out[1]!=2 || !bytes.Equal(out[56:64],[]byte{0xfe,0xff,0xff,0xff,4,3,2,1}) {t.Fatal("bad link reply")}
	if !bytes.Equal(out[80:],[]byte{2,0,0x30,0x39,8,8,8,8}) {t.Fatal("bad compact address")}
	if in[1]!=1 {t.Fatal("modified input")}
	if linkReply(in[:20],netip.MustParseAddrPort("192.168.1.2:1234"))!=nil {t.Fatal("accepted truncated frame")}
}

func TestMediaReassembly(t *testing.T) {
	var c mediaChannel
	data:=binary.BigEndian.AppendUint32(nil,3)
	data=append(data,'a','b','c')
	if ok,m:=c.push(1,data[3:]);!ok || len(m)!=0 {t.Fatal("unexpected message")}
	if ok,m:=c.push(0,data[:3]);!ok || len(m)!=1 || string(m[0])!="abc" {t.Fatal("failed out-of-order reassembly")}
	if ok,m:=c.push(1,data[3:]);!ok || len(m)!=0 {t.Fatal("duplicate replayed")}
}

func TestCommandFrame(t *testing.T) {
	f:=commandFrame(1,0x100,[]byte("{}"))
	if len(f)!=18 || f[0]!=0xf1 || f[1]!=0xd0 || binary.BigEndian.Uint32(f[8:12])!=6 || binary.BigEndian.Uint32(f[12:16])!=0x100 {t.Fatal("bad command framing")}
}
