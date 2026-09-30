package dsmconf

import (
	"strings"
	"testing"
)

// The list names the ramdisk's own model and version with the build's .pat
// address and MD5, in the fields synoupgrade reads.
//
// 목록은 램디스크 자신의 모델·버전과 빌드의 .pat 주소·MD5 를, synoupgrade 가
// 읽는 칸에 담는다.
func TestInstallRSS(t *testing.T) {
	version := "majorversion=\"7\"\nminorversion=\"4\"\nproductversion=\"7.4.1\"\nbuildnumber=\"90080\"\nbuilddate=\"2026/07/22\"\n"
	synoinfo := "unique=\"synology_apollolake_918+\"\n"
	url := "https://global.synologydownload.com/download/DSM/release/7.4.1/90080/DSM_DS918+_90080.pat"
	got, err := InstallRSS(version, synoinfo, url, "6de559b7a28ba66fc28981504bf36717")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<rss version="2.0">`,
		"<title>DSM 7.4.1-90080</title>",
		"<MajorVer>7</MajorVer>",
		"<MinorVer>4</MinorVer>",
		"<BuildNum>90080</BuildNum>",
		"<BuildDate>2026/07/22</BuildDate>",
		"<mUnique>synology_apollolake_918+</mUnique>",
		"<mLink>" + url + "</mLink>",
		"<mCheckSum>6de559b7a28ba66fc28981504bf36717</mCheckSum>",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if _, err := InstallRSS(version, "", url, "x"); err == nil {
		t.Error("no unique: want an error")
	}
}

// RenderRelease writes nothing without both values, and ParseRelease reads back
// what it wrote.
//
// RenderRelease 는 두 값이 다 있어야 쓰고, ParseRelease 는 쓴 것을 다시 읽는다.
func TestRelease(t *testing.T) {
	if RenderRelease("http://x/a.pat", "") != nil || RenderRelease("", "abc") != nil {
		t.Fatal("wrote a release without its url or md5")
	}
	url, md5 := ParseRelease(string(RenderRelease("http://x/a.pat", "abc")))
	if url != "http://x/a.pat" || md5 != "abc" {
		t.Fatalf("got %q %q", url, md5)
	}
}
