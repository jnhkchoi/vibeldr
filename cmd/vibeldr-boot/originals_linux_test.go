//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestOriginalsFromHDAReadsPastTheTarEnd builds an xz tar the way hda1.tgz is
// laid out - modules under usr/lib/modules - with more than a pipe's worth of
// data after the tar's end marker, and reads it with the system xz. The reader
// has to drain xz's output before waiting for it; otherwise xz blocks writing
// and the install stops there for good.
//
// TestOriginalsFromHDAReadsPastTheTarEnd - hda1.tgz 처럼 usr/lib/modules 아래
// 모듈이 있는 xz tar 를, tar 끝 표시 뒤에 파이프 하나보다 많은 데이터를 두고
// 만들어 시스템 xz 로 읽는다. 읽는 쪽이 xz 출력을 다 비운 뒤에 기다려야 한다 -
// 아니면 xz 가 쓰기에서 막히고 설치가 거기서 영영 멈춘다.
func TestOriginalsFromHDAReadsPastTheTarEnd(t *testing.T) {
	xz, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("no xz on this machine")
	}
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range []struct{ name, body string }{
		{"./etc/VERSION", "x"},
		{"./usr/lib/modules/e1000e.ko", "e1000e"},
		{"./usr/lib/modules/ptp.ko", "ptp"},
		{"./usr/lib/firmware/x.bin", "fw"},
	} {
		tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(e.body))
	}
	tw.Close()
	raw.Write(make([]byte, 1<<20))

	dir := t.TempDir()
	hda := filepath.Join(dir, hdaName)
	cmd := exec.Command(xz, "-c")
	cmd.Stdin = bytes.NewReader(raw.Bytes())
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hda, out, 0o644); err != nil {
		t.Fatal(err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := originalsFromHDA(hda, xz, "")
		done <- result{len(p), err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.n != 2 {
			t.Fatalf("got %d modules, err %v; want 2", r.n, r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("originalsFromHDA did not return: xz and the reader are waiting on each other")
	}
}
