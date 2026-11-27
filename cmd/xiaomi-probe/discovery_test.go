package main

import (
	"encoding/json"
	"testing"
)

func TestCollectHomeIDs(t *testing.T) {
	var h homeEntry
	if err:=json.Unmarshal([]byte(`{"id":"1","uid":123,"dids":["device-a",""],"roomlist":[{"dids":["device-b","device-a"]}]}`),&h);err!=nil {t.Fatal(err)}
	out:=map[string]bool{}
	collectHomeIDs(h,out)
	if len(out)!=2 || !out["device-a"] || !out["device-b"] {t.Fatal("unexpected device collection")}
}
