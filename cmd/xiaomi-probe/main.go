// xiaomi-probe is a read-only provisioning diagnostic. HLS is opt-in.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/xiaomi"
	"github.com/AlexxIT/go2rtc/pkg/xiaomi/crypto"
	"gopkg.in/yaml.v3"
)

type device struct {
	DID string `json:"did"`
	Model string `json:"model"`
}

type vendorResponse struct {
	Vendor struct {
		ID int `json:"vendor"`
		Params struct {
			UID string `json:"p2p_id"`
			Init string `json:"init_string"`
			License string `json:"license"`
			CRC string `json:"crc_key"`
		} `json:"vendor_params"`
	} `json:"vendor"`
	Public string `json:"public_key"`
	Sign string `json:"sign"`
}

type report struct {
	Model string `json:"model"`
	Number int `json:"device_number"`
	Vendor string `json:"vendor,omitempty"`
	UID bool `json:"has_p2p_id"`
	Init bool `json:"has_init_string"`
	License bool `json:"has_license"`
	CRC bool `json:"has_crc_key"`
	Key bool `json:"has_device_key"`
	Sign bool `json:"has_signature"`
	Provision string `json:"provision"`
	HLS string `json:"hls,omitempty"`
	HLSShape string `json:"hls_shape,omitempty"`
	Relay string `json:"relay,omitempty"`
	RelayServers int `json:"relay_servers,omitempty"`
	RelayCandidates int `json:"relay_candidates,omitempty"`
	MISS string `json:"miss,omitempty"`
	Media string `json:"media,omitempty"`
	VideoFrames int `json:"video_frames,omitempty"`
	VideoCodec string `json:"video_codec,omitempty"`
	Channel int `json:"channel,omitempty"`
	VideoPackets bool `json:"video_packets"`
	AudioPackets bool `json:"audio_packets"`
}

func vendorName(id int) string {
	switch id {
	case 1: return "tutk"
	case 3: return "agora"
	case 4: return "cs2"
	case 6: return "mtp"
	default: return fmt.Sprintf("unknown(%d)",id)
	}
}

func request(c *xiaomi.Cloud, base, path string, payload any) ([]byte,error) {
	b,err:=json.Marshal(payload)
	if err!=nil {return nil,err}
	return c.Request(base,path,string(b),nil)
}

func provision(c *xiaomi.Cloud,base string,d device,r *report, relay, media bool, channel int) {
	pub,priv,err:=crypto.GenerateKey()
	if err!=nil {r.Provision="key_generation_failed";return}
	b,err:=request(c,base,"/v2/device/miss_get_vendor",map[string]string{
		"did":d.DID,"app_pubkey":fmt.Sprintf("%x",pub),"support_vendors":"TUTK_CS2_MTP",
	})
	if err!=nil {r.Provision="cloud_request_failed";return}
	var v vendorResponse
	if json.Unmarshal(b,&v)!=nil || v.Vendor.ID==0 {r.Provision="invalid_response";return}
	r.Vendor=vendorName(v.Vendor.ID)
	r.UID=v.Vendor.Params.UID!=""
	r.Init=v.Vendor.Params.Init!=""
	r.License=v.Vendor.Params.License!=""
	r.CRC=v.Vendor.Params.CRC!=""
	r.Key=v.Public!=""
	r.Sign=v.Sign!=""
	r.Provision="response_received"
	if (relay || media) && v.Vendor.ID==4 {
		var onReady func(*net.UDPConn,netip.AddrPort)
		if media {
			r.Channel=channel
			onReady=func(s *net.UDPConn,peer netip.AddrPort) {probeMedia(s,peer,v,pub,priv,channel,r)}
		}
		r.Relay,r.RelayServers,r.RelayCandidates=probeRelay(v.Vendor.Params.UID,v.Vendor.Params.Init,onReady)
	}
}

// Accept object, singleton array, and nested result responses without exposing values.
func hlsURL(b []byte) (string,string) {
	var root any
	if json.Unmarshal(b,&root)!=nil {return "","invalid_action_response"}
	for depth:=0;depth<4;depth++ {
		switch v:=root.(type) {
		case []any:
			if len(v)!=1 {return "","unexpected_action_array"}
			root=v[0]
		case map[string]any:
			if code,ok:=v["code"];ok && code!=nil && fmt.Sprint(code)!="0" {
				return "","action_rejected"
			}
			if next,ok:=v["result"];ok {root=next;continue}
			out,ok:=v["out"].([]any)
			if !ok {return "","missing_action_output"}
			for _,item:=range out {
				value,ok:=item.(string)
				if m,isMap:=item.(map[string]any);isMap {
					if fmt.Sprint(m["piid"])!="5" {continue}
					value,ok=m["value"].(string)
				}
				if !ok {continue}
				u,err:=url.Parse(value)
				if err==nil && u.Host!="" && (u.Scheme=="https" || u.Scheme=="http") {return value,"url_received"}
			}
			return "","no_http_stream_url"
		default: return "","invalid_action_response"
		}
	}
	return "","invalid_action_response"
}

func responseShape(b []byte) string {
	var v any
	if json.Unmarshal(b,&v)!=nil {return "invalid_json"}
	switch x:=v.(type) {
	case []any: return fmt.Sprintf("array:%d",len(x))
	case map[string]any:
		// Only known field names and value types, never values.
		var keys []string
		for _,k:=range []string{"code","result","out","message","did","siid","aiid"} {
			if val,ok:=x[k];ok {keys=append(keys,fmt.Sprintf("%s:%T",k,val))}
		}
		return "object:"+strings.Join(keys,",")
	default: return fmt.Sprintf("%T",v)
	}
}

func probeHLS(c *xiaomi.Cloud,base string,d device,r *report) {
	if d.Model!="chuangmi.camera.ipc019" {r.HLS="not_implemented_for_model";return}
	ffprobe,err:=exec.LookPath("ffprobe")
	if err!=nil {r.HLS="ffprobe_not_installed";return}
	// IPC019 Google Home HLS service: SIID 4, AIID 1, auto quality.
	b,err:=request(c,base,"/miotspec/action",map[string]any{
		"params":map[string]any{"did":d.DID,"siid":4,"aiid":1,"in":[]any{0}},
	})
	if err!=nil {r.HLS="cloud_action_failed";return}
	r.HLSShape=responseShape(b)
	stream,status:=hlsURL(b)
	r.HLS=status
	if stream=="" {return}
	ctx,cancel:=context.WithTimeout(context.Background(),60*time.Second)
	defer cancel()
	// Never print the signed URL or ffprobe stderr. Do not issue stop-stream:
	// it may interfere with an existing viewer.
	out,err:=exec.CommandContext(ctx,ffprobe,"-v","error","-protocol_whitelist","https,http,tcp,tls,crypto","-read_intervals","%+5","-count_packets","-show_entries","stream=codec_type,nb_read_packets","-of","json",stream).Output()
	if ctx.Err()!=nil {r.HLS="media_timeout";return}
	if err!=nil {r.HLS="media_probe_failed";return}
	var result struct {
		Streams []struct {
			Type string `json:"codec_type"`
			Count string `json:"nb_read_packets"`
		} `json:"streams"`
	}
	if json.Unmarshal(out,&result)!=nil {r.HLS="media_response_invalid";return}
	for _,s:=range result.Streams {
		var n int
		if _,err:=fmt.Sscan(s.Count,&n);err!=nil || n<=0 {continue}
		if s.Type=="video" {r.VideoPackets=true}
		if s.Type=="audio" {r.AudioPackets=true}
	}
	if r.VideoPackets {r.HLS="video_packets_received"} else {r.HLS="no_video_packets"}
}

func run() error {
	cfg:=flag.String("config","go2rtc.yaml","existing go2rtc configuration file")
	account:=flag.String("account","","Xiaomi user ID (required for multiple accounts)")
	region:=flag.String("region","cn","account region")
	models:=flag.String("models","isa.camera.cw501d,chuangmi.camera.ipc019,chuangmi.camera.069a01,chuangmi.camera.81ac1","comma-separated model filter")
	hls:=flag.Bool("hls",false,"opt in to IPC019 cloud HLS and ffprobe")
	relay:=flag.Bool("relay",false,"opt in to CS2 remote relay handshake")
	media:=flag.Bool("media",false,"opt in to MISS authentication and short remote media test")
	channel:=flag.Int("channel",1,"video channel (1 or 2)")
	inventory:=flag.Bool("inventory",false,"print camera model counts only")
	did:=flag.String("did","","optional authorized device ID for targeted discovery (not printed)")
	flag.Parse()
	if *channel!=1 && *channel!=2 {return fmt.Errorf("channel must be 1 or 2")}
	if *media && *hls {return fmt.Errorf("test media and HLS separately")}
	allowed:=map[string]bool{"cn":true,"de":true,"i2":true,"in":true,"ru":true,"sg":true,"tw":true,"us":true}
	if !allowed[*region] {return fmt.Errorf("unsupported region")}
	base:="https://api.io.mi.com/app"
	if *region!="cn" {base="https://"+*region+".api.io.mi.com/app"}
	data,err:=os.ReadFile(*cfg)
	if err!=nil {return fmt.Errorf("cannot read configuration")}
	var config struct { Xiaomi map[string]string `yaml:"xiaomi"` }
	if yaml.Unmarshal(data,&config)!=nil {return fmt.Errorf("invalid configuration")}
	valid:=map[string]string{}
	for id,token:=range config.Xiaomi {if strings.TrimSpace(id)!="" && strings.TrimSpace(token)!="" {valid[id]=token}}
	if *account=="" && len(valid)==1 {for id:=range valid {*account=id}}
	token:=valid[*account]
	if token=="" {return fmt.Errorf("select an existing nonempty Xiaomi account with -account")}
	cloud:=xiaomi.NewCloud("xiaomiio")
	if cloud.LoginWithToken(*account,token)!=nil {return fmt.Errorf("Xiaomi token login failed; refresh login in go2rtc")}
	devices,err:=discoverDevices(cloud,base,strings.TrimSpace(*did))
	if err!=nil {return err}
	if *inventory {
		counts:=map[string]int{}
		for _,d:=range devices {if strings.Contains(d.Model,".camera.") {counts[d.Model]++}}
		keys:=make([]string,0,len(counts))
		for k:=range counts {keys=append(keys,k)}
		sort.Strings(keys)
		for _,k:=range keys {fmt.Printf("%s %d\n",k,counts[k])}
		return nil
	}
	selected:=map[string]bool{}
	for _,m:=range strings.Split(*models,",") {selected[strings.TrimSpace(m)]=true}
	count:=map[string]int{}
	enc:=json.NewEncoder(os.Stdout)
	for _,d:=range devices {
		if !selected[d.Model] || d.DID=="" || (*did!="" && d.DID!=strings.TrimSpace(*did)) {continue}
		count[d.Model]++
		r:=report{Model:d.Model,Number:count[d.Model]}
		provision(cloud,base,d,&r,*relay,*media,*channel)
		if *hls {probeHLS(cloud,base,d,&r)}
		if err:=enc.Encode(r);err!=nil {return err}
	}
	for _,m:=range strings.Split(*models,",") {
		m=strings.TrimSpace(m)
		if m!="" && count[m]==0 {
			if err:=enc.Encode(report{Model:m,Provision:"not_in_discovered_devices"});err!=nil {return err}
		}
	}
	return nil
}

func main() {
	if err:=run();err!=nil {fmt.Fprintln(os.Stderr,err);os.Exit(1)}
}
