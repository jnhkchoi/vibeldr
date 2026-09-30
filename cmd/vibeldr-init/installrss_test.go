package main

import (
	"net"
	"testing"
)

// The port follows the scheme when the address has none.
// 주소에 포트가 없으면 스킴을 따른다.
func TestPatHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://global.synologydownload.com/download/DSM/release/7.4.1/90080/DSM_DS918+_90080.pat": "global.synologydownload.com:443",
		"http://mirror.lan/DSM.pat":      "mirror.lan:80",
		"http://mirror.lan:8080/DSM.pat": "mirror.lan:8080",
		"":                               "",
	} {
		if got := patHost(in); got != want {
			t.Errorf("patHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// A listening port is reachable and a closed one is not, and the answer is
// kept rather than asked again.
//
// 듣고 있는 포트는 닿고 닫힌 포트는 닿지 않으며, 답은 다시 묻지 않고 기억한다.
func TestReachability(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	up := reachability(addr)
	if !up() {
		t.Fatal("listening port not reachable")
	}
	l.Close()
	if !up() {
		t.Fatal("the remembered answer was not kept")
	}
	if reachability(addr)() {
		t.Fatal("closed port reachable")
	}
}
