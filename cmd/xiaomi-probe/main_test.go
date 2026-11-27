package main

import "testing"

func TestVendorName(t *testing.T) {
	for id,want:=range map[int]string{1:"tutk",3:"agora",4:"cs2",6:"mtp",8:"unknown(8)"} {
		if got:=vendorName(id);got!=want {t.Errorf("vendor %d: got %s want %s",id,got,want)}
	}
}

func TestHLSURL(t *testing.T) {
	for _,tc:=range []struct{input,url,status string}{
		{`{"code":0,"out":[{"piid":5,"value":"https://example.invalid/stream.m3u8"}]}`,"https://example.invalid/stream.m3u8","url_received"},
		{`{"code":-1}`,"","action_code_-1"},
		{`{"out":[{"piid":5,"value":"file:///etc/passwd"}]}`,"","no_http_stream_url"},
		{`{"out":[{"piid":4,"value":"https://example.invalid"}]}`,"","no_http_stream_url"},
		{`{"out":[{"piid":5,"value":"https:/broken"}]}`,"","no_http_stream_url"},
		{"invalid","","invalid_action_response"},
	} {
		u,s:=hlsURL([]byte(tc.input))
		if u!=tc.url || s!=tc.status {t.Errorf("got (%q,%q) want (%q,%q)",u,s,tc.url,tc.status)}
	}
}
