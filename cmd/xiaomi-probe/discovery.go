package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/AlexxIT/go2rtc/pkg/xiaomi"
)

// Discover through the paginated account list and the merged home list.
// A failed secondary endpoint is reported but does not discard valid devices.
func discoverDevices(c *xiaomi.Cloud,base string, targetDID string) ([]device,error) {
	all:=map[string]device{}
	add:=func(list []device) {for _,d:=range list {if d.DID!="" && d.Model!="" {all[d.DID]=d}}}
	type page struct {
		List []device `json:"list"`
		More bool `json:"has_more"`
		Next string `json:"next_start_did"`
	}
	p:=map[string]any{"limit":200,"get_split_device":true,"get_third_device":true,"dids":[]string{}}
	success:=false
	seen:=map[string]bool{}
	for i:=0;i<50;i++ {
		b,err:=request(c,base,"/v2/home/device_list_page",p)
		var v page
		if err!=nil || json.Unmarshal(b,&v)!=nil {fmt.Fprintln(os.Stderr,"discovery: account page failed");break}
		success=true
		add(v.List)
		if !v.More {break}
		if v.Next=="" || seen[v.Next] {fmt.Fprintln(os.Stderr,"discovery: pagination incomplete");break}
		seen[v.Next]=true;p["start_did"]=v.Next
	}
	b,err:=request(c,base,"/home/device_list",map[string]any{"getVirtualModel":true,"getHuamiDevices":1,"support_smart_home":true})
	var legacy page
	if err==nil && json.Unmarshal(b,&legacy)==nil {success=true;add(legacy.List)} else {fmt.Fprintln(os.Stderr,"discovery: legacy list failed")}
	// Keep home identifiers as raw JSON, preserving integer precision.
	var homes struct {
		Homes []homeEntry `json:"homelist"`
		Shared []homeEntry `json:"share_home_list"`
	}
	b,err=request(c,base,"/v2/homeroom/gethome_merged",map[string]any{"fg":true,"fetch_share":true,"fetch_share_dev":true,"limit":300,"app_ver":7,"plat_form":0})
	if err==nil && json.Unmarshal(b,&homes)==nil {
		for _,h:=range append(homes.Homes,homes.Shared...) {
			if len(h.ID)==0 || len(h.UID)==0 {continue}
			start:=""
			seen:=map[string]bool{}
			for i:=0;i<50;i++ {
				b,err:=request(c,base,"/v2/home/home_device_list",map[string]any{"home_id":h.ID,"home_owner":h.UID,"limit":300,"start_did":start,"get_split_device":false,"support_smart_home":true,"get_third_device":true})
				var v struct { Devices []device `json:"device_info"`; More bool `json:"has_more"`; Next string `json:"max_did"` }
				if err!=nil || json.Unmarshal(b,&v)!=nil {fmt.Fprintln(os.Stderr,"discovery: home page failed");break}
				success=true;add(v.Devices)
				if !v.More {break}
				if v.Next=="" || seen[v.Next] {fmt.Fprintln(os.Stderr,"discovery: home pagination incomplete");break}
				start=v.Next;seen[start]=true
			}
		}
	} else {fmt.Fprintln(os.Stderr,"discovery: merged home list failed")}
	// Official HA also queries the unmerged home endpoint and resolves DIDs
	// from shared homes/rooms through the account's device-detail API.
	ids:=map[string]bool{}
	for _,h:=range append(homes.Homes,homes.Shared...) {collectHomeIDs(h,ids)}
	raw,e:=request(c,base,"/v2/homeroom/gethome",map[string]any{"limit":150,"fetch_share":true,"fetch_share_dev":true,"plat_form":0,"app_ver":9})
	if e==nil {
		var h struct {
			Homes []homeEntry `json:"homelist"`
			Shared []homeEntry `json:"share_home_list"`
			More bool `json:"has_more"`
		}
		if json.Unmarshal(raw,&h)==nil {
			for _,v:=range append(h.Homes,h.Shared...) {collectHomeIDs(v,ids)}
			if h.More {fmt.Fprintln(os.Stderr,"discovery: home room list may be incomplete")}
		} else {fmt.Fprintln(os.Stderr,"discovery: home list response invalid")}
	} else {fmt.Fprintln(os.Stderr,"discovery: home list failed")}
	if targetDID!="" {ids[targetDID]=true}
	keys:=make([]string,0,len(ids))
	for id:=range ids {keys=append(keys,id)}
	sort.Strings(keys)
	for start:=0;start<len(keys);start+=150 {
		end:=min(start+150,len(keys))
		params:=map[string]any{"limit":200,"get_split_device":true,"get_third_device":true,"dids":keys[start:end]}
		seen:=map[string]bool{}
		for i:=0;i<50;i++ {
			b,e:=request(c,base,"/v2/home/device_list_page",params)
			var v page
			if e!=nil || json.Unmarshal(b,&v)!=nil {fmt.Fprintln(os.Stderr,"discovery: targeted device list failed");break}
			success=true;add(v.List)
			if !v.More {break}
			if v.Next=="" || seen[v.Next] {fmt.Fprintln(os.Stderr,"discovery: targeted pagination incomplete");break}
			seen[v.Next]=true;params["start_did"]=v.Next
		}
	}
	if !success {return nil,fmt.Errorf("all device list requests failed")}
	out:=make([]device,0,len(all))
	for _,d:=range all {out=append(out,d)}
	sort.Slice(out,func(i,j int)bool{return out[i].DID<out[j].DID})
	return out,nil
}

// Both owned and shared homes can expose devices directly or in rooms.
type homeEntry struct {
	ID json.RawMessage `json:"id"`
	UID json.RawMessage `json:"uid"`
	DIDs []string `json:"dids"`
	Rooms []struct { DIDs []string `json:"dids"` } `json:"roomlist"`
}

func collectHomeIDs(h homeEntry, dst map[string]bool) {
	for _,did:=range h.DIDs {if did!="" {dst[did]=true}}
	for _,room:=range h.Rooms {for _,did:=range room.DIDs {if did!="" {dst[did]=true}}}
}
