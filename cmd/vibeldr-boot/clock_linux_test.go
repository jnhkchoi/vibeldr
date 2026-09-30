//go:build linux

package main

import (
	"encoding/binary"
	"testing"
	"time"
)

// A server answer's transmit time is read back to the nanosecond scale it
// carries; client packets and unsynchronised servers are refused.
//
// 서버 답의 송신 시각을 담긴 정밀도대로 읽는다. 클라이언트 패킷과 동기화 안 된
// 서버는 거절한다.
func TestParseSNTP(t *testing.T) {
	want := time.Date(2026, 9, 26, 12, 0, 0, 500_000_000, time.UTC)
	b := make([]byte, 48)
	b[0] = 0x1c // version 3, mode 4 (server) / 버전 3, 모드 4 (서버)
	b[1] = 2
	binary.BigEndian.PutUint32(b[40:44], uint32(want.Unix()+ntpEpochOffset))
	binary.BigEndian.PutUint32(b[44:48], 1<<31)
	got, err := parseSNTP(b)
	if err != nil || !got.Equal(want) {
		t.Fatalf("got %v, %v", got, err)
	}
	b[1] = 0
	if _, err := parseSNTP(b); err == nil {
		t.Fatal("stratum 0 accepted")
	}
	b[1], b[0] = 2, 0x1b
	if _, err := parseSNTP(b); err == nil {
		t.Fatal("client packet accepted")
	}
}
