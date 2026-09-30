//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"
)

// Setting the clock before anything is downloaded.
//
// The install fetches the driver pack from GitHub and the .pat from Synology
// over HTTPS, and a certificate is only valid between two dates. A machine
// whose CMOS battery is flat, or whose BIOS was reset, boots years in the past,
// every certificate looks not yet valid, and the install stops at its first
// download with an error that says nothing about the clock.
//
// So once the network is up the time is asked of an NTP server, one SNTP
// packet over UDP, and the system clock is set when it is off by more than a
// minute. Only the running system's clock changes; the hardware clock is left
// as it is.
//
// 무엇이든 내려받기 전에 시계를 맞춘다.
//
// 설치는 드라이버 팩을 GitHub 에서, .pat 을 시놀로지에서 HTTPS 로 받는데, 인증서는
// 두 날짜 사이에서만 유효하다. CMOS 배터리가 다 됐거나 BIOS 가 초기화된 기계는
// 몇 년 전 날짜로 부팅하고, 모든 인증서가 아직 유효하지 않은 것으로 보여 설치가
// 첫 다운로드에서 멈춘다. 오류는 시계에 대해 아무 말도 하지 않는다.
//
// 그래서 네트워크가 올라오면 NTP 서버에 시각을 묻고 (UDP 로 SNTP 패킷 하나),
// 1 분 넘게 틀리면 시스템 시계를 맞춘다. 도는 시스템의 시계만 바꾸고 하드웨어
// 시계는 그대로 둔다.

// ntpServers are asked in order until one answers.
// ntpServers - 하나가 답할 때까지 차례로 묻는다.
var ntpServers = []string{"pool.ntp.org:123", "time.google.com:123", "time.cloudflare.com:123"}

// ntpEpochOffset is the seconds from 1900 (NTP's epoch) to 1970 (Unix's).
// ntpEpochOffset - 1900 년(NTP 기준)에서 1970 년(유닉스 기준)까지의 초.
const ntpEpochOffset = 2208988800

// syncClock sets the clock from the first NTP server that answers and returns
// what it did, for the log.
//
// syncClock - 처음 답하는 NTP 서버로 시계를 맞추고, 로그용으로 한 일을 돌려준다.
func syncClock() (string, error) {
	var lastErr error
	for _, server := range ntpServers {
		t, err := sntpTime(server, 3*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		off := time.Until(t)
		if off > -time.Minute && off < time.Minute {
			return fmt.Sprintf("clock: already right (%s)", server), nil
		}
		tv := syscall.NsecToTimeval(t.UnixNano())
		if err := syscall.Settimeofday(&tv); err != nil {
			return "", fmt.Errorf("set the clock: %w", err)
		}
		return fmt.Sprintf("clock: moved %s to %s (%s)", off.Round(time.Second), t.UTC().Format(time.RFC3339), server), nil
	}
	return "", fmt.Errorf("no time server answered: %w", lastErr)
}

// sntpTime sends one SNTP client request and reads the server's transmit time.
// sntpTime - SNTP 클라이언트 요청을 하나 보내고 서버의 송신 시각을 읽는다.
func sntpTime(server string, timeout time.Duration) (time.Time, error) {
	conn, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := make([]byte, 48)
	req[0] = 0x1b // LI 0, version 3, mode 3 (client) / LI 0, 버전 3, 모드 3 (클라이언트)
	if _, err := conn.Write(req); err != nil {
		return time.Time{}, err
	}
	resp := make([]byte, 48)
	n, err := conn.Read(resp)
	if err != nil {
		return time.Time{}, err
	}
	return parseSNTP(resp[:n])
}

// parseSNTP reads the transmit timestamp out of a 48-byte SNTP answer.
// parseSNTP - 48 바이트 SNTP 답에서 송신 시각을 읽는다.
func parseSNTP(b []byte) (time.Time, error) {
	if len(b) < 48 {
		return time.Time{}, fmt.Errorf("short answer (%d bytes)", len(b))
	}
	if mode := b[0] & 7; mode != 4 {
		return time.Time{}, fmt.Errorf("not a server answer (mode %d)", mode)
	}
	if b[1] == 0 {
		return time.Time{}, fmt.Errorf("server not synchronised (stratum 0)")
	}
	sec := binary.BigEndian.Uint32(b[40:44])
	frac := binary.BigEndian.Uint32(b[44:48])
	if sec < ntpEpochOffset {
		return time.Time{}, fmt.Errorf("time before 1970")
	}
	ns := int64(frac) * 1e9 >> 32
	return time.Unix(int64(sec)-ntpEpochOffset, ns), nil
}
