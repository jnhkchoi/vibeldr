package main

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"vibeldr/internal/dsmconf"
)

// serveInstallRSS serves the release list the build left in the ramdisk
// (dsmconf/rss.go) at dsmconf.InstallRSSAddr, where the ramdisk's
// synoinfo.conf points synoupgrade. A ramdisk without the list keeps the stock
// update server, and this returns at once.
//
// The list is only handed out while the .pat's server can be reached. The
// installer takes an answer as "the internet is there" and then downloads the
// .pat itself; with no way to reach it, a 503 leaves the installer at its
// manual upload, as Synology's list would when it cannot be fetched.
//
// serveInstallRSS - 빌드가 램디스크에 남긴 릴리스 목록(dsmconf/rss.go)을
// dsmconf.InstallRSSAddr 에서 준다. 램디스크의 synoinfo.conf 가 synoupgrade 를
// 거기로 보낸다. 목록이 없는 램디스크는 원래 업데이트 서버를 쓰고, 이 함수는
// 바로 돌아온다.
//
// 목록은 .pat 서버에 닿을 수 있을 때만 준다. 설치기는 답을 받으면 "인터넷이
// 있다"고 보고 .pat 을 직접 받는다. 닿을 길이 없으면 503 으로, 시놀로지 목록을
// 받지 못했을 때처럼 설치기를 수동 업로드에 둔다.
func serveInstallRSS() {
	list, err := os.ReadFile("/" + dsmconf.InstallRSSName)
	if err != nil {
		return
	}
	raw, _ := os.ReadFile("/" + dsmconf.ReleaseName)
	link, _ := dsmconf.ParseRelease(string(raw))
	host := patHost(link)
	up := reachability(host)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if host != "" && !up() {
			http.Error(w, "the .pat server cannot be reached", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(list)
	})
	logf("install RSS: serving %s at %s", dsmconf.InstallRSSName, dsmconf.InstallRSSAddr)
	if err := http.ListenAndServe(dsmconf.InstallRSSAddr, mux); err != nil {
		logf("install RSS: %v", err)
	}
}

// patHost is the host:port of the .pat address, "" when it has none.
// patHost - .pat 주소의 host:port. 없으면 "".
func patHost(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// reachability returns a check that a TCP connection to host can be opened,
// remembering the answer for half a minute: get_state.cgi asks on every poll
// of the installer page.
//
// reachability - host 로 TCP 연결을 열 수 있는지 보는 검사를 돌려준다. 답은
// 30 초 동안 기억한다. get_state.cgi 는 설치 화면이 물을 때마다 묻는다.
func reachability(host string) func() bool {
	var (
		mu   sync.Mutex
		at   time.Time
		last bool
	)
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < 30*time.Second {
			return last
		}
		c, err := net.DialTimeout("tcp", host, 5*time.Second)
		if err == nil {
			c.Close()
		}
		last, at = err == nil, time.Now()
		return last
	}
}
