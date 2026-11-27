// xiaomi-probe is a read-only provisioning diagnostic. HLS is opt-in.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
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

func provision(c *xiaomi.Cloud,base string,d device,r *report) {
	pub,_,err:=crypto.GenerateKey()
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
}

func hlsURL(b []byte) (string,string) {
	var v struct {
		Code *int `json:"code"`
		Out []struct {
			PIID int `json:"piid"`
			Value string `json:"value"`
		} `json:"out"`
	}
	if json.Unmarshal(b,&v)!=nil {return "","invalid_action_response"}
	if v.Code!=nil && *v.Code!=0 {return "",fmt.Sprintf("action_code_%d",*v.Code)}
	for _,item:=range v.Out {
		if item.PIID!=5 {continue}
		u,err:=url.Parse(item.Value)
		if err==nil && u.Host!="" && (u.Scheme=="https" || u.Scheme=="http") {return item.Value,"url_received"}
	}
	return "","no_http_stream_url"
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
	flag.Parse()
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
	b,err:=request(cloud,base,"/v2/home/device_list_page",map[string]any{})
	if err!=nil {return fmt.Errorf("device list request failed")}
	var devices struct {List []device `json:"list"`}
	if json.Unmarshal(b,&devices)!=nil {return fmt.Errorf("invalid device list")}
	selected:=map[string]bool{}
	for _,m:=range strings.Split(*models,",") {selected[strings.TrimSpace(m)]=true}
	count:=map[string]int{}
	enc:=json.NewEncoder(os.Stdout)
	for _,d:=range devices.List {
		if !selected[d.Model] || d.DID=="" {continue}
		count[d.Model]++
		r:=report{Model:d.Model,Number:count[d.Model]}
		provision(cloud,base,d,&r)
		if *hls {probeHLS(cloud,base,d,&r)}
		if err:=enc.Encode(r);err!=nil {return err}
	}
	for _,m:=range strings.Split(*models,",") {
		m=strings.TrimSpace(m)
		if m!="" && count[m]==0 {
			if err:=enc.Encode(report{Model:m,Provision:"not_in_first_device_page"});err!=nil {return err}
		}
	}
	return nil
}

func main() {
	if err:=run();err!=nil {fmt.Fprintln(os.Stderr,err);os.Exit(1)}
}
