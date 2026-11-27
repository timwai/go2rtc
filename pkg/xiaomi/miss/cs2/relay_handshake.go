package cs2

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Wire constants documented by the CS2/PPPP protocol research.
// This implementation performs only a bounded relay join, not MISS media.
const initKey = "4959433db5bf6da347534f6165e371e9677f02030badb3892b2f35c16b8b959711e5a70deff1050783fb9d3bc5c713171d1f2529d3df"

func publicIPv4(ip netip.Addr) bool {
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

func relayServers(init string) []netip.AddrPort {
	enc,_,_:=strings.Cut(init,":")
	if len(enc)==0 || len(enc)>4096 || len(enc)%2!=0 {return nil}
	key,_:=hex.DecodeString(initKey)
	decoded:=make([]byte,len(enc)/2)
	state:=byte(57)
	for i:=range decoded {
		a,b:=enc[2*i],enc[2*i+1]
		if a<'A' || a>'P' || b<'A' || b>'P' {return nil}
		decoded[i]=state^key[i%len(key)]^((a-'A')<<4 | (b-'A'))
		state^=decoded[i]
	}
	var servers []netip.AddrPort
	seen:=map[netip.Addr]bool{}
	for _,part:=range strings.Split(string(decoded),",") {
		ip,err:=netip.ParseAddr(strings.TrimSpace(part))
		if err!=nil || !publicIPv4(ip) || seen[ip] {continue}
		seen[ip]=true
		servers=append(servers,netip.AddrPortFrom(ip,32100))
		if len(servers)>=8 {break}
	}
	return servers
}

func encodeUID(s string) []byte {
	p:=strings.Split(s,"-")
	if len(p)!=3 || len(p[0])==0 || len(p[0])>8 || len(p[2])==0 || len(p[2])>8 {return nil}
	n,e:=strconv.ParseUint(p[1],10,32)
	if e!=nil {return nil}
	b:=make([]byte,20)
	copy(b,p[0]);binary.BigEndian.PutUint32(b[8:],uint32(n));copy(b[12:],p[2])
	return b
}

func relayFrame(kind byte,payload []byte) []byte {
	b:=[]byte{0xf1,kind,0,0}
	binary.BigEndian.PutUint16(b[2:],uint16(len(payload)))
	return append(b,payload...)
}

func decodeAddr(p []byte) netip.AddrPort {
	if len(p)<16 || binary.BigEndian.Uint16(p)!=2 {return netip.AddrPort{}}
	ip:=netip.AddrFrom4([4]byte{p[7],p[6],p[5],p[4]})
	port:=binary.LittleEndian.Uint16(p[2:])
	if !publicIPv4(ip) || port==0 {return netip.AddrPort{}}
	return netip.AddrPortFrom(ip,port)
}

func encodeAddr(a netip.AddrPort) []byte {
	p:=make([]byte,16)
	binary.BigEndian.PutUint16(p,2)
	binary.LittleEndian.PutUint16(p[2:],a.Port())
	ip:=a.Addr().As4()
	copy(p[4:],[]byte{ip[3],ip[2],ip[1],ip[0]})
	return p
}

type relayMessage struct { From netip.AddrPort; Kind byte; Body []byte }

func readRelay(s *net.UDPConn,until time.Time,handle func(relayMessage) bool) bool {
	_ = s.SetReadDeadline(until)
	b:=make([]byte,65536)
	for time.Now().Before(until) {
		n,a,e:=s.ReadFromUDPAddrPort(b)
		if e!=nil {return false}
		if n<4 || b[0]!=0xf1 || int(binary.BigEndian.Uint16(b[2:]))!=n-4 {continue}
		if handle(relayMessage{a,b[1],b[4:n]}) {return true}
	}
	return false
}

// dialRelaySocket transfers ownership of the negotiated UDP socket to the caller.
func dialRelaySocket(uid, init string) (*net.UDPConn, netip.AddrPort, error) {
	var socket *net.UDPConn
	var peer netip.AddrPort
	var status string
	for attempt:=0;attempt<3;attempt++ {
		status,_,_=relayAttempt(uid,init,func(s *net.UDPConn,a netip.AddrPort){socket=s;peer=a})
		if socket!=nil {return socket,peer,nil}
		if status=="invalid_relay_parameters" || status=="udp_open_failed" {break}
		if attempt<2 {time.Sleep(time.Duration(attempt+1)*time.Second)}
	}
	return nil,netip.AddrPort{},fmt.Errorf("cs2: %s",status)
}

func relayAttempt(uidString,init string,onReady func(*net.UDPConn,netip.AddrPort)) (string,int,int) {
	servers:=relayServers(init)
	uid:=encodeUID(uidString)
	if len(servers)==0 || uid==nil {return "invalid_relay_parameters",len(servers),0}
	s,e:=net.ListenUDP("udp4",nil)
	if e!=nil {return "udp_open_failed",len(servers),0}
	owned:=false
	defer func(){if !owned {_=s.Close()}}()
	send:=func(a netip.AddrPort,k byte,p []byte) {_,_=s.WriteToUDPAddrPort(relayFrame(k,p),a)}
	serverIPs:=map[netip.Addr]bool{}
	for _,a:=range servers {serverIPs[a.Addr()]=true}
	type token [4]byte
	candidates:=map[netip.AddrPort]map[token]bool{}
	for attempt:=0;attempt<3 && len(candidates)==0;attempt++ {
		for _,a:=range servers {send(a,0x67,uid)}
		readRelay(s,time.Now().Add(2500*time.Millisecond),func(m relayMessage)bool{
			if !serverIPs[m.From.Addr()] || m.Kind!=0x82 || len(m.Body)<20 {return false}
			a:=decodeAddr(m.Body)
			if !a.IsValid() {return false}
			if len(candidates)>=32 && candidates[a]==nil {return false}
			if candidates[a]==nil {candidates[a]=map[token]bool{}}
			if len(candidates[a])<16 {candidates[a][token(m.Body[16:20])]=true}
			return false
		})
	}
	n:=len(candidates)
	if n==0 {return "no_relay_candidates",len(servers),n}
	register:=func(a netip.AddrPort,t token,repeats int) {
		p:=make([]byte,28);copy(p,t[:]);copy(p[4:],uid)
		for i:=0;i<repeats;i++ {send(a,0x83,p)}
	}
	var chosen netip.AddrPort
	for i:=0;i<4 && !chosen.IsValid();i++ {
		for a,tokens:=range candidates {
			for t:=range tokens {register(a,t,4);time.Sleep(2*time.Millisecond)}
		}
		time.Sleep(100*time.Millisecond)
		for a:=range candidates {send(a,0x70,nil);time.Sleep(2*time.Millisecond)}
		readRelay(s,time.Now().Add(2500*time.Millisecond),func(m relayMessage)bool {
			if m.Kind!=0x71 {return false}
			for a:=range candidates {
				if m.From.Addr()==a.Addr() {chosen=netip.AddrPortFrom(a.Addr(),25000);return true}
			}
			return false
		})
	}
	if !chosen.IsValid() {return "relay_hello_timeout",len(servers),n}
	var session netip.AddrPort
	var sessionToken token
	for i:=0;i<4 && !session.IsValid();i++ {
		send(chosen,0x72,nil)
		readRelay(s,time.Now().Add(2*time.Second),func(m relayMessage)bool{
			if m.From!=chosen || m.Kind!=0x73 || len(m.Body)<6 {return false}
			port:=binary.BigEndian.Uint16(m.Body[4:])
			if port==0 {return false}
			sessionToken=token(m.Body[:4]);session=netip.AddrPortFrom(chosen.Addr(),port)
			return true
		})
	}
	if !session.IsValid() {return "relay_port_timeout",len(servers),n}
	body:=append(append(append([]byte{},uid...),encodeAddr(session)...),sessionToken[:]...)
	for i:=0;i<6;i++ {
		for _,a:=range servers {send(a,0x80,body)}
		register(session,sessionToken,24)
		if readRelay(s,time.Now().Add(2*time.Second),func(m relayMessage)bool{return m.Kind==0x84 && m.From==session}) {
			if onReady!=nil {owned=true;onReady(s,session)}
			return "relay_ready",len(servers),n
		}
	}
	return "relay_join_timeout",len(servers),n
}
