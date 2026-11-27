package cs2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

type mediaChannel struct {
	next uint16
	pending map[uint16][]byte
	buf []byte
}

func (c *mediaChannel) push(seq uint16,data []byte) (bool,[][]byte) {
	delta:=int16(seq-c.next)
	if delta<0 {return true,nil}
	if delta>512 {return false,nil}
	if c.pending==nil {c.pending=make(map[uint16][]byte)}
	if _,exists:=c.pending[seq];!exists {
		c.pending[seq]=bytes.Clone(data)
	}
	var messages [][]byte
	for {
		part,ok:=c.pending[c.next]
		if !ok {break}
		delete(c.pending,c.next);c.next++
		c.buf=append(c.buf,part...)
		for len(c.buf)>=4 {
			n:=int(binary.BigEndian.Uint32(c.buf))
			if n<1 || n>8*1024*1024 {c.buf=nil;return false,nil}
			if len(c.buf)<4+n {break}
			messages=append(messages,bytes.Clone(c.buf[4:4+n]))
			c.buf=c.buf[4+n:]
		}
	}
	return true,messages
}

func linkReply(p []byte,local netip.AddrPort) []byte {
	if len(p)!=88 || p[0]!=0xa1 || p[1]!=1 || !local.Addr().Is4() {return nil}
	if binary.BigEndian.Uint16(p[40:])!=2 {return nil}
	out:=bytes.Clone(p)
	out[1]=2
	copy(out[56:],[]byte{0xfe,0xff,0xff,0xff,p[7],p[6],p[5],p[4]})
	copy(out[64:],encodeAddr(local))
	out[80],out[81]=2,0
	out[82],out[83]=p[43],p[42]
	copy(out[84:],[]byte{p[47],p[46],p[45],p[44]})
	return out
}

func commandFrame(seq uint16,cmd uint32,payload []byte) []byte {
	p:=[]byte{0xd1,0,0,0}
	binary.BigEndian.PutUint16(p[2:],seq)
	p=binary.BigEndian.AppendUint32(p,uint32(4+len(payload)))
	p=binary.BigEndian.AppendUint32(p,cmd)
	return relayFrame(0xd0,append(p,payload...))
}

func localForRelay(s *net.UDPConn,peer netip.AddrPort) netip.AddrPort {
	c,e:=net.DialUDP("udp4",nil,net.UDPAddrFromAddrPort(peer))
	if e!=nil {return netip.AddrPort{}}
	defer c.Close()
	ip:=c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	return netip.AddrPortFrom(ip,s.LocalAddr().(*net.UDPAddr).AddrPort().Port())
}


// RelayConn implements the MISS transport interface over the native CS2 relay.
type RelayConn struct {
	socket *net.UDPConn
	peer netip.AddrPort
	local netip.AddrPort
	linked chan struct{}
	done chan struct{}
	cmds chan []byte
	packets chan []byte
	acks chan uint16
	closeOnce sync.Once
	cmdMu sync.Mutex
	seq uint16
}

func DialRelay(uid, init string) (*RelayConn,error) {
	s,peer,err:=dialRelaySocket(uid,init)
	if err!=nil {return nil,err}
	local:=localForRelay(s,peer)
	if !local.IsValid() {_=s.Close();return nil,errors.New("cs2: local route failed")}
	c:=&RelayConn{socket:s,peer:peer,local:local,linked:make(chan struct{}),done:make(chan struct{}),cmds:make(chan []byte,32),packets:make(chan []byte,128),acks:make(chan uint16,64)}
	_ = s.SetReadDeadline(time.Now().Add(20*time.Second))
	go c.worker()
	t:=time.NewTimer(15*time.Second)
	defer t.Stop()
	select {
	case <-c.linked: return c,nil
	case <-c.done: return nil,io.EOF
	case <-t.C: _=c.Close();return nil,errors.New("cs2: relay link timeout")
	}
}

func (c *RelayConn) Protocol() string {return "cs2+relay"}
func (c *RelayConn) Version() string {return "CS2 Relay"}
func (c *RelayConn) RemoteAddr() net.Addr {return net.UDPAddrFromAddrPort(c.peer)}
func (c *RelayConn) SetDeadline(t time.Time) error {
	// Bound writes independently; clearing a read deadline must not stall Stop.
	return c.socket.SetReadDeadline(t)
}
func (c *RelayConn) send(kind byte,body []byte) error {
	_,err:=c.socket.WriteToUDPAddrPort(relayFrame(kind,body),c.peer)
	return err
}
func (c *RelayConn) Close() error {
	c.closeOnce.Do(func(){
		_ = c.socket.SetWriteDeadline(time.Now().Add(time.Second))
		_ = c.send(0xf0,nil)
		_ = c.socket.Close()
	})
	return nil
}

func (c *RelayConn) worker() {
	defer close(c.done)
	defer c.Close()
	var ch0,ch2 mediaChannel
	linked:=false
	b:=make([]byte,65536)
	for {
		n,a,err:=c.socket.ReadFromUDPAddrPort(b)
		if err!=nil {return}
		if a!=c.peer || n<4 || b[0]!=0xf1 || int(binary.BigEndian.Uint16(b[2:]))!=n-4 {continue}
		p:=b[4:n]
		switch b[1] {
		case 0x28:
			if reply:=linkReply(p,c.local);reply!=nil {
				if c.send(0x28,reply)!=nil {return}
				if !linked {linked=true;close(c.linked)}
			}
		case 0xe0: if c.send(0xe1,nil)!=nil {return}
		case 0xf0: _=c.send(0xf1,nil);return
		case 0xd1:
			if len(p)<4 || p[0]!=0xd1 || p[1]!=0 {continue}
			count:=int(binary.BigEndian.Uint16(p[2:]))
			if count>(len(p)-4)/2 {continue}
			for i:=0;i<count;i++ {
				select {case c.acks<-binary.BigEndian.Uint16(p[4+i*2:]):default:}
			}
		case 0xd0:
			if len(p)<4 || p[0]!=0xd1 {continue}
			var target *mediaChannel
			var queue chan []byte
			switch p[1] {case 0:target=&ch0;queue=c.cmds;case 2:target=&ch2;queue=c.packets;default:continue}
			ok,msgs:=target.push(binary.BigEndian.Uint16(p[2:]),p[4:])
			if ok {if c.send(0xd1,[]byte{0xd1,p[1],0,1,p[2],p[3]})!=nil {return}}
			for _,msg:=range msgs {
				select {
				case queue<-msg:
				default:
					// An unconsumed control notification must not stall media.
					// A full media queue instead terminates the session for reconnect.
					if p[1]==2 {return}
				}
			}
		}
	}
}

func (c *RelayConn) ReadCommand() (uint32,[]byte,error) {
	select {
	case msg:=<-c.cmds:
		if len(msg)<4 {return 0,nil,errors.New("cs2: short command")}
		cmd:=binary.BigEndian.Uint32(msg)
		if cmd>0xffff {cmd=binary.LittleEndian.Uint32(msg)}
		return cmd,msg[4:],nil
	case <-c.done: return 0,nil,io.EOF
	}
}

func (c *RelayConn) WriteCommand(cmd uint32,p []byte) error {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	if len(p)>1100 {return errors.New("cs2: command too large")}
	seq:=c.seq;c.seq++
	frame:=commandFrame(seq,cmd,p)
	for i:=0;i<5;i++ {
		if _,err:=c.socket.WriteToUDPAddrPort(frame,c.peer);err!=nil {return err}
		t:=time.NewTimer(time.Second)
	wait:
		for {
			select {
			case ack:=<-c.acks: if ack==seq {t.Stop();return nil}
			case <-c.done: t.Stop();return io.EOF
			case <-t.C: break wait
			}
		}
	}
	return fmt.Errorf("cs2: command %x acknowledgement timeout",cmd)
}
func (c *RelayConn) ReadPacket() ([]byte,[]byte,error) {
	select {
	case msg:=<-c.packets:
		if len(msg)<40 {return nil,nil,errors.New("cs2: short media packet")}
		return msg[:32],msg[32:],nil
	case <-c.done: return nil,nil,io.EOF
	}
}
func (c *RelayConn) WritePacket(_, _ []byte) error {return errors.New("cs2: relay talkback not implemented")}
