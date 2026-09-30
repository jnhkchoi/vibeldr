package dsmconf

import (
	"reflect"
	"testing"
)

// The platform layer overrides the loader's policy and the user overrides
// both.
//
// 플랫폼 층은 로더 정책을, 사용자는 둘 다를 덮어쓴다.
func TestSettingsLayers(t *testing.T) {
	got := Settings(
		map[string]string{"netif_seq": "", "rss_server": "platform"},
		map[string]string{"rss_server": "user", "maxlanport": "4"},
	)
	want := map[string]string{
		"support_disk_compatibility": "yes",
		"rss_server":                 "user",
		"rss_server_ssl":             LoaderPolicy["rss_server_ssl"],
		"rss_server_v2":              LoaderPolicy["rss_server_v2"],
		"netif_seq":                  "",
		"maxlanport":                 "4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if LoaderPolicy["rss_server"] == "user" {
		t.Fatal("Settings wrote into LoaderPolicy")
	}
}

// What RenderList writes, ParseList reads back in the same sorted order,
// including an empty value.
//
// RenderList 가 쓴 것을 ParseList 가 같은 정렬 순서로, 빈 값까지 다시 읽는다.
func TestRenderParseList(t *testing.T) {
	raw := RenderList(map[string]string{"b": "2", "a": "x=y", "netif_seq": ""})
	if string(raw) != "a=x=y\nb=2\nnetif_seq=\n" {
		t.Fatalf("rendered %q", raw)
	}
	got := ParseList("# comment\n\n" + string(raw) + "=novalue\n")
	want := [][2]string{{"a", "x=y"}, {"b", "2"}, {"netif_seq", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed %v", got)
	}
	if RenderList(nil) != nil {
		t.Fatal("empty settings rendered to something")
	}
}
