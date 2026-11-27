package miss

import (
	"encoding/binary"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func avccNALU(nalu []byte) []byte {
	b:=make([]byte,4+len(nalu))
	binary.BigEndian.PutUint32(b,uint32(len(nalu)))
	copy(b[4:],nalu)
	return b
}

func TestH265ParameterSetsCanArriveSeparately(t *testing.T) {
	var sets videoParameterSets
	sets.add(avccNALU([]byte{64,1,1}),codecH265) // VPS
	if sets.codec(codecH265)!=nil {t.Fatal("codec ready without SPS/PPS")}
	sets.add(avccNALU([]byte{66,1,2,3,4,5,6,7}),codecH265) // SPS
	if sets.codec(codecH265)!=nil {t.Fatal("codec ready without PPS")}
	sets.add(avccNALU([]byte{68,1,1}),codecH265) // PPS
	got:=sets.codec(codecH265)
	if got==nil || got.Name!=core.CodecH265 {t.Fatal("HEVC codec not assembled")}
}

func TestH264ParameterSetsCanArriveSeparately(t *testing.T) {
	var sets videoParameterSets
	sets.add(avccNALU([]byte{0x67,0x64,0,0x29}),codecH264)
	if sets.codec(codecH264)!=nil {t.Fatal("codec ready without PPS")}
	sets.add(avccNALU([]byte{0x68,0xee,0x3c,0x80}),codecH264)
	got:=sets.codec(codecH264)
	if got==nil || got.Name!=core.CodecH264 {t.Fatal("H264 codec not assembled")}
}
