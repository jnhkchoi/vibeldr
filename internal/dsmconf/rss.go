package dsmconf

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// The installer's online install.
//
// The web installer offers to download and install DSM by itself only when its
// get_state.cgi reports internet_ok, and that is the answer of
// `synoupgrade --check`: it fetches the release list at rss_server_ssl and
// looks for this model's unique name in it. Synology's list for 7.4.1-90080
// has no synology_apollolake_918+, so a DS918+ installer is always told there
// is no internet, and on the other models the list offers whatever release is
// newest, which need not be one the loader was built for.
//
// So the build writes a list of its own into the ramdisk (InstallRSSName): one
// release, the one the loader was built for, with the .pat address and MD5
// the loader itself downloads with. The ramdisk's synoinfo.conf points
// rss_server_ssl at InstallRSSURL, and the helper serves the list there while
// the installer runs (cmd/vibeldr-init/installrss.go).
//
// 설치기의 온라인 설치.
//
// 웹 설치기는 get_state.cgi 가 internet_ok 를 알릴 때만 DSM 을 스스로 받아
// 설치하겠다고 한다. 그 값은 `synoupgrade --check` 의 답이다. rss_server_ssl
// 에서 릴리스 목록을 받아 이 모델의 unique 이름을 찾는다. 시놀로지의 7.4.1-90080
// 목록에는 synology_apollolake_918+ 가 없어서 DS918+ 설치기는 늘 인터넷이
// 없다고 듣고, 다른 모델에서는 목록이 가장 새 릴리스를 내놓는데 그것이 로더가
// 빌드된 릴리스라는 보장이 없다.
//
// 그래서 빌드가 램디스크에 자기 목록을 쓴다 (InstallRSSName). 릴리스는 로더가
// 빌드된 것 하나이고, .pat 주소와 MD5 는 로더 자신이 받을 때 쓰는 값이다.
// 램디스크의 synoinfo.conf 는 rss_server_ssl 을 InstallRSSURL 로 돌리고,
// 설치기가 도는 동안 헬퍼가 거기서 목록을 준다
// (cmd/vibeldr-init/installrss.go).

const (
	// ReleaseName carries the .pat address and MD5 the build used, as a
	// RenderList list with the keys url and md5.
	//
	// ReleaseName - 빌드가 쓴 .pat 주소와 MD5 를 url, md5 키의 RenderList
	// 목록으로 실어 나른다.
	ReleaseName = "vibeldr-release"

	// InstallRSSName is the release list, at the ramdisk root.
	// InstallRSSName - 릴리스 목록. 램디스크 루트에 있다.
	InstallRSSName = "vibeldr-rss.xml"

	// InstallRSSAddr is where the helper serves the list, on the loopback only.
	// InstallRSSAddr - 헬퍼가 목록을 주는 곳. 루프백에서만 연다.
	InstallRSSAddr = "127.0.0.1:18470"

	// InstallRSSURL is the value rss_server_ssl gets in the ramdisk.
	// synoupgrade takes a plain http address here as well.
	//
	// InstallRSSURL - 램디스크에서 rss_server_ssl 이 갖는 값. synoupgrade 는
	// 여기에 평범한 http 주소도 받는다.
	InstallRSSURL = "http://" + InstallRSSAddr + "/autoupdate/genRSS.php"
)

// RenderRelease writes ReleaseName's contents, or nothing when either value is
// missing: an entry without its MD5 is no use to synoupgrade.
//
// RenderRelease - ReleaseName 의 내용을 쓴다. 둘 중 하나라도 없으면 아무것도
// 쓰지 않는다. MD5 없는 항목은 synoupgrade 에 쓸모가 없다.
func RenderRelease(url, md5 string) []byte {
	url, md5 = strings.TrimSpace(url), strings.TrimSpace(md5)
	if url == "" || md5 == "" {
		return nil
	}
	return RenderList(map[string]string{"url": url, "md5": md5})
}

// ParseRelease reads what RenderRelease wrote.
// ParseRelease - RenderRelease 가 쓴 것을 읽는다.
func ParseRelease(raw string) (url, md5 string) {
	for _, kv := range ParseList(raw) {
		switch kv[0] {
		case "url":
			url = kv[1]
		case "md5":
			md5 = kv[1]
		}
	}
	return url, md5
}

// rssItem is one release in synoupgrade's list. Of the fields Synology's list
// has, synoupgrade needs title, MajorVer, MinorVer, BuildNum and the model
// entry; without MajorVer and MinorVer the DS918+ 7.4.1 installer's
// synoupgrade reports "The RSS file parse fail".
//
// rssItem - synoupgrade 목록의 릴리스 하나. 시놀로지 목록에 있는 칸 가운데
// synoupgrade 에 필요한 것은 title, MajorVer, MinorVer, BuildNum 과 모델
// 항목이다. MajorVer 와 MinorVer 가 없으면 DS918+ 7.4.1 설치기의 synoupgrade 가
// "The RSS file parse fail" 을 낸다.
type rssItem struct {
	Title     string `xml:"title"`
	MajorVer  string `xml:"MajorVer"`
	MinorVer  string `xml:"MinorVer"`
	BuildNum  string `xml:"BuildNum"`
	BuildDate string `xml:"BuildDate,omitempty"`
	Model     struct {
		Unique   string `xml:"mUnique"`
		Link     string `xml:"mLink"`
		CheckSum string `xml:"mCheckSum"`
	} `xml:"model"`
}

type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

// InstallRSS builds the list from the ramdisk's own VERSION file and
// synoinfo.conf (their text) and the release the build used.
//
// InstallRSS - 램디스크 자신의 VERSION 파일과 synoinfo.conf (그 내용), 빌드가
// 쓴 릴리스로 목록을 만든다.
func InstallRSS(version, synoinfo, url, md5 string) ([]byte, error) {
	get := func(body, key string) string {
		v, _ := Get(body, key)
		return strings.TrimSpace(v)
	}
	var it rssItem
	product, build := get(version, "productversion"), get(version, "buildnumber")
	it.MajorVer, it.MinorVer = get(version, "majorversion"), get(version, "minorversion")
	it.BuildDate = get(version, "builddate")
	it.Model.Unique = get(synoinfo, "unique")
	it.Model.Link, it.Model.CheckSum = url, md5
	if product == "" || build == "" || it.MajorVer == "" || it.MinorVer == "" || it.Model.Unique == "" || url == "" || md5 == "" {
		return nil, fmt.Errorf("missing version, model or release (product %q build %q unique %q)", product, build, it.Model.Unique)
	}
	it.Title = "DSM " + product + "-" + build
	it.BuildNum = build

	var doc rss
	doc.Version = "2.0"
	doc.Channel.Title = "RSS for DSM Auto Update"
	doc.Channel.Items = []rssItem{it}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}
