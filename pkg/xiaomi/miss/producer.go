package miss

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

type Producer struct {
	core.Connection
	client *Client
}

func Dial(rawURL string) (core.Producer, error) {
	client, err := NewClient(rawURL)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: connect/auth: %w",err)
	}

	u, _ := url.Parse(rawURL)
	query := u.Query()

	err = client.StartMedia(query.Get("channel"), query.Get("subtype"), query.Get("audio"))
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("xiaomi: start media: %w",err)
	}

	medias, err := probe(client, query.Get("audio") != "0")
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Producer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "xiaomi/miss",
			Protocol:   client.Protocol(),
			RemoteAddr: client.RemoteAddr().String(),
			UserAgent:  client.Version(),
			Medias:     medias,
			Transport:  client,
		},
		client: client,
	}, nil
}

type videoParameterSets struct {
	vps []byte
	sps []byte
	pps []byte
}

func (s *videoParameterSets) add(avcc []byte, codec uint32) {
	for len(avcc)>=4 {
		n:=int(binary.BigEndian.Uint32(avcc[:4]))
		if n<=0 || n>len(avcc)-4 {return}
		nalu:=avcc[4:4+n]
		if len(nalu)>0 {
			var typ byte
			if codec==codecH264 {typ=nalu[0]&0x1f} else {typ=(nalu[0]>>1)&0x3f}
			switch {
			case codec==codecH264 && typ==h264.NALUTypeSPS:
				s.sps=bytes.Clone(nalu)
			case codec==codecH264 && typ==h264.NALUTypePPS:
				s.pps=bytes.Clone(nalu)
			case codec==codecH265 && typ==h265.NALUTypeVPS:
				s.vps=bytes.Clone(nalu)
			case codec==codecH265 && typ==h265.NALUTypeSPS:
				s.sps=bytes.Clone(nalu)
			case codec==codecH265 && typ==h265.NALUTypePPS:
				s.pps=bytes.Clone(nalu)
			}
		}
		avcc=avcc[4+n:]
	}
}

func (s *videoParameterSets) codec(codec uint32) *core.Codec {
	switch codec {
	case codecH264:
		if len(s.sps)>0 && len(s.pps)>0 {
			return h264.AVCCToCodec(h264.JoinNALU(s.sps,s.pps))
		}
	case codecH265:
		if len(s.vps)>0 && len(s.sps)>0 && len(s.pps)>0 {
			return h265.AVCCToCodec(h264.JoinNALU(s.vps,s.sps,s.pps))
		}
	}
	return nil
}

func probe(client *Client, audio bool) ([]*core.Media, error) {
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	var vcodec, acodec *core.Codec
	var h264Sets,h265Sets videoParameterSets
	var videoReadyAt time.Time

	for {
		pkt, err := client.ReadPacket()
		if err != nil {
			if vcodec != nil {
				err = fmt.Errorf("video ready but audio codec was not detected")
			}
			return nil, fmt.Errorf("xiaomi: probe: %w", err)
		}

		switch pkt.CodecID {
		case codecH264:
			h264Sets.add(annexb.EncodeToAVCC(pkt.Payload),codecH264)
			if vcodec==nil {vcodec=h264Sets.codec(codecH264)}
		case codecH265:
			h265Sets.add(annexb.EncodeToAVCC(pkt.Payload),codecH265)
			if vcodec==nil {vcodec=h265Sets.codec(codecH265)}
		case codecPCM:
			if acodec==nil {acodec=&core.Codec{Name:core.CodecPCML,ClockRate:pkt.SampleRate()}}
		case codecPCMU:
			if acodec==nil {acodec=&core.Codec{Name:core.CodecPCMU,ClockRate:pkt.SampleRate()}}
		case codecPCMA:
			if acodec==nil {acodec=&core.Codec{Name:core.CodecPCMA,ClockRate:pkt.SampleRate()}}
		case codecOPUS:
			if acodec==nil {acodec=&core.Codec{Name:core.CodecOpus,ClockRate:48000,Channels:2}}
		}

		if vcodec!=nil && videoReadyAt.IsZero() {videoReadyAt=time.Now()}
		// A camera may expose an audio codec unsupported by this go2rtc version.
		// Do not let optional audio prevent an otherwise valid video stream from opening.
		if vcodec!=nil && (!audio || acodec!=nil || time.Since(videoReadyAt)>=2*time.Second) {
			break
		}
	}

	_ = client.SetDeadline(time.Time{})

	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{vcodec},
		},
	}

	if acodec != nil {
		medias = append(medias, &core.Media{
			Kind:      core.KindAudio,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{acodec},
		})

		if client.Protocol()!="cs2+relay" {
			medias = append(medias, &core.Media{
				Kind: core.KindAudio, Direction: core.DirectionSendonly, Codecs: []*core.Codec{acodec.Clone()},
			})
		}
	}

	return medias, nil
}

const timestamp40ms = 48000 * 0.040

func (p *Producer) Start() error {
	var audioTS uint32

	for {
		_ = p.client.SetDeadline(time.Now().Add(10 * time.Second))
		pkt, err := p.client.ReadPacket()
		if err != nil {
			return err
		}

		p.Recv += len(pkt.Payload)

		// TODO: rewrite this
		var name string
		var pkt2 *core.Packet

		switch pkt.CodecID {
		case codecH264, codecH265:
			pkt2 = &core.Packet{
				Header: rtp.Header{
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      TimeToRTP(pkt.Timestamp, 90000),
				},
				Payload: annexb.EncodeToAVCC(pkt.Payload),
			}
			if pkt.CodecID == codecH264 {
				name = core.CodecH264
			} else {
				name = core.CodecH265
			}
		case codecPCM:
			name = core.CodecPCML
			pkt2 = &core.Packet{Header:rtp.Header{Version:2,Marker:true,SequenceNumber:uint16(pkt.Sequence),Timestamp:audioTS},Payload:pkt.Payload}
			audioTS += uint32(len(pkt.Payload)/2)
		case codecPCMU:
			name = core.CodecPCMU
			pkt2 = &core.Packet{Header:rtp.Header{Version:2,Marker:true,SequenceNumber:uint16(pkt.Sequence),Timestamp:audioTS},Payload:pkt.Payload}
			audioTS += uint32(len(pkt.Payload))
		case codecPCMA:
			name = core.CodecPCMA
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			audioTS += uint32(len(pkt.Payload))
		case codecOPUS:
			name = core.CodecOpus
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			// known cameras sends packets with 40ms long
			audioTS += timestamp40ms
		}

		for _, recv := range p.Receivers {
			if recv.Codec.Name == name {
				recv.WriteRTP(pkt2)
				break
			}
		}
	}
}

func (p *Producer) Stop() error {
	_ = p.client.StopMedia()
	return p.Connection.Stop()
}

// TimeToRTP convert time in milliseconds to RTP time
func TimeToRTP(timeMS, clockRate uint64) uint32 {
	return uint32(timeMS * clockRate / 1000)
}
