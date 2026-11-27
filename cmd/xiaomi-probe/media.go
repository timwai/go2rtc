package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"

	"github.com/AlexxIT/go2rtc/pkg/xiaomi/crypto"
)

// The probe uses bounded, ordered DRW reassembly and never persists video.
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

func probeMedia(s *net.UDPConn,peer netip.AddrPort,v vendorResponse,pub,priv []byte,channel,quality int,r *report) {
	r.MISS="link_timeout"
	local:=localForRelay(s,peer)
	if !local.IsValid() {r.MISS="local_route_failed";return}
	if len(v.Public)!=64 || len(v.Sign)==0 {r.MISS="invalid_credentials";return}
	key,e:=crypto.CalcSharedKey(v.Public,hex.EncodeToString(priv))
	if e!=nil {r.MISS="invalid_device_key";return}
	send:=func(kind byte,body []byte) {_,_=s.WriteToUDPAddrPort(relayFrame(kind,body),peer)}
	defer send(0xf0,nil)
	var ch0,ch2 mediaChannel
	linked,auth,started:=false,false,false
	var pending []byte
	var pendingSeq uint16
	var retryAt time.Time
	authBody,_:=json.Marshal(map[string]any{"public_key":hex.EncodeToString(pub),"sign":v.Sign,"uuid":"","support_encrypt":15})
	setCommand:=func(seq uint16,cmd uint32,p []byte) {
		pendingSeq=seq;pending=commandFrame(seq,cmd,p)
		_,_=s.WriteToUDPAddrPort(pending,peer)
		retryAt=time.Now().Add(time.Second)
	}
	end:=time.Now().Add(45*time.Second)
	var mediaEnd time.Time
	for time.Now().Before(end) {
		if started && time.Now().After(mediaEnd) {break}
		if pending!=nil && time.Now().After(retryAt) {
			_,_=s.WriteToUDPAddrPort(pending,peer)
			retryAt=time.Now().Add(time.Second)
		}
		readRelay(s,time.Now().Add(350*time.Millisecond),func(m relayMessage)bool{
			if m.From!=peer {return false}
			switch m.Kind {
			case 0x28:
				reply:=linkReply(m.Body,local)
				if reply!=nil {
					send(0x28,reply)
					if !linked {
						linked=true;r.MISS="auth_timeout"
						setCommand(0,0x100,authBody)
					}
				}
			case 0xe0: send(0xe1,nil)
			case 0xf0: send(0xf1,nil);end=time.Now()
			case 0xd1:
				p:=m.Body
				if len(p)>=4 && p[0]==0xd1 && p[1]==0 {
					n:=int(binary.BigEndian.Uint16(p[2:]))
					if n<=(len(p)-4)/2 {
						for i:=0;i<n;i++ {if binary.BigEndian.Uint16(p[4+i*2:])==pendingSeq {pending=nil}}
					}
				}
			case 0xd0:
				p:=m.Body
				if len(p)<4 || p[0]!=0xd1 {return false}
				seq:=binary.BigEndian.Uint16(p[2:])
				var target *mediaChannel
				switch p[1] {case 0:target=&ch0;case 2:target=&ch2;default:return false}
				ok,msgs:=target.push(seq,p[4:])
				if ok {send(0xd1,[]byte{0xd1,p[1],0,1,p[2],p[3]})}
				for _,msg:=range msgs {
					if p[1]==0 && !auth && len(msg)>=4 {
						cmd:=binary.BigEndian.Uint32(msg)
						if cmd!=0x101 && binary.LittleEndian.Uint32(msg)!=0x101 {continue}
						var a struct {Result string `json:"result"`}
						if json.Unmarshal(msg[4:],&a)!=nil || a.Result!="success" {r.MISS="auth_rejected";end=time.Now();return true}
						auth=true;r.MISS="authenticated";pending=nil
						body:=map[string]int{"videoquality":quality,"enableaudio":1}
						if channel==2 {body["videoquality"]=-1;body["videoquality2"]=quality}
						j,_:=json.Marshal(body)
						enc,e:=crypto.Encode(append([]byte{0,0,1,2},j...),key)
						if e!=nil {r.MISS="encrypt_failed";end=time.Now();return true}
						setCommand(1,0x1001,enc)
						started=true;mediaEnd=time.Now().Add(20*time.Second)
						r.Media="no_video_packets"
					} else if p[1]==2 && started && len(msg)>=40 {
						codec:=binary.LittleEndian.Uint32(msg[4:])
						data,e:=crypto.Decode(msg[32:],key)
						if e!=nil {continue}
						switch codec {
						case 4,5:
							// Count only recognizable Annex-B video, not arbitrary decrypted bytes.
							if bytes.HasPrefix(data,[]byte{0,0,1}) || bytes.HasPrefix(data,[]byte{0,0,0,1}) {
								r.VideoPackets=true;r.VideoFrames++;r.Media="video_packets_received"
								if codec==4 {r.VideoCodec="h264"} else {r.VideoCodec="h265";inspectHEVC(data,r)}
							}
						case 1024,1026,1027,1032:
							if len(data)>0 {
								r.AudioPackets=true
								switch codec {
								case 1024: r.AudioCodec="pcm"
								case 1026: r.AudioCodec="pcmu"
								case 1027: r.AudioCodec="pcma"
								case 1032: r.AudioCodec="opus"
								}
							}
						}
					}
				}
			}
			return true
		})
	}
}

func inspectHEVC(annexB []byte,r *report) {
	avcc:=annexb.EncodeToAVCC(annexB)
	for len(avcc)>=4 {
		n:=int(binary.BigEndian.Uint32(avcc[:4]))
		if n<=0 || n>len(avcc)-4 {return}
		nalu:=avcc[4:4+n]
		if len(nalu)>16 && (nalu[0]>>1)&0x3f==h265.NALUTypeSPS {
			if sps:=h265.DecodeSPS(nalu);sps!=nil {
				r.VideoWidth=int(sps.Width());r.VideoHeight=int(sps.Height())
				return
			}
		}
		avcc=avcc[4+n:]
	}
}
