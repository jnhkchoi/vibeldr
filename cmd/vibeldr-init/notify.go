package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// It is common for the user not to find the loader's IP after installing.
// announceLoop only prints to the console, and in a headless setup there is no
// screen to read. This pushes the boot events outward instead:
//
//	boot_started - vibeldr-init has started running in the ramdisk boot
//	dsm_up       - the agent inside DSM has finished the storage verdict
//	error        - a ramdisk step (drivers, boot device) failed; the boot
//	               carries on
//
// Two channels are supported in parallel:
//
//	webhook: a JSON POST of the event's fields to an HTTP endpoint. The body
//	         is not shaped for any one service.
//	email:   a text mail over SMTP, with STARTTLS when the server offers it.
//
// A failure never blocks the boot; this is best effort. With not one config
// field filled in, it returns quietly without even trying.
//
// 설치 후 사용자가 로더의 IP 를 찾지 못하는 사례가 많다. 콘솔에 찍히는
// announceLoop 만으로는 헤드리스 환경에서 화면을 볼 수 없기 때문이다.
// 여기서는 부팅 이벤트를 외부로 밀어 준다:
//
//	boot_started - vibeldr-init 이 램디스크 부팅에서 돌기 시작했다
//	dsm_up       - DSM 안의 에이전트가 스토리지 판정을 마쳤다
//	error        - 램디스크 단계 (드라이버, 부트 장치) 가 실패했다. 부팅은
//	               계속된다
//
// 두 채널을 병렬로 지원한다:
//
//	webhook: 이벤트 필드를 JSON 으로 HTTP 엔드포인트에 POST. 본문은 특정
//	         서비스에 맞춘 형태가 아니다.
//	email:   SMTP 텍스트 메일. 서버가 제공하면 STARTTLS 를 쓴다.
//
// 실패해도 부팅은 절대 막지 않는다 (best-effort). config 필드가 하나도
// 채워져 있지 않으면 시도조차 하지 않고 조용히 return 한다.

// notifyConfigPaths are where the notification settings file is looked for,
// tried front first: /vibeldr-notify.json in the ramdisk stage and
// /etc/vibeldr/notify.json inside DSM after the pivot. No build step writes
// either file (loader.yaml's notify section is not carried here), so unless one
// is put there by hand, notify does nothing.
//
// notifyConfigPaths - 알림 설정 파일을 찾는 자리들. 앞의 것부터 시도한다.
// 램디스크 단계는 /vibeldr-notify.json, pivot 후 DSM 안에서는
// /etc/vibeldr/notify.json 이다. 어느 빌드 단계도 이 파일을 쓰지 않으므로
// (loader.yaml 의 notify 는 여기로 실려 오지 않는다), 손으로 놓지 않는 한
// notify 는 아무 일도 하지 않는다.
var notifyConfigPaths = []string{
	"/vibeldr-notify.json",
	"/etc/vibeldr/notify.json",
}

// notifyHTTPTimeout is the HTTP POST timeout, short enough not to hold the boot up.
// notifyHTTPTimeout - HTTP POST 타임아웃. 부팅을 붙잡지 않을 만큼 짧게 잡는다.
const notifyHTTPTimeout = 5 * time.Second

// notifySMTPTimeout is the SMTP connection timeout, kept short for the same reason.
// notifySMTPTimeout - SMTP 연결 타임아웃. 마찬가지로 짧게 잡는다.
const notifySMTPTimeout = 10 * time.Second

// notifyConfig is the notification settings carried in as JSON. Its fields
// correspond to Notify in loader.yaml, but it is declared again here to avoid a
// circular dependency - this package does not import config.
//
// notifyConfig - JSON 으로 실려온 알림 설정. loader.yaml 의 Notify 와
// 필드가 대응하지만, 순환 의존을 피하려고 여기서 별도로 재선언한다
// (이 패키지에서 config 를 import 하지 않기 위함).
type notifyConfig struct {
	WebhookURL string `json:"webhook_url"`
	Email      struct {
		SMTP     string `json:"smtp"`     // host:port
		From     string `json:"from"`     // sender address / 발신 주소
		To       string `json:"to"`       // recipients, comma separated / 수신 (쉼표 구분)
		Username string `json:"username"` // AUTH user / AUTH 사용자
		Password string `json:"password"` // AUTH password / AUTH 비밀번호
	} `json:"email"`
}

// notify pushes one event out over whichever channels are configured. With no
// channel active it does nothing and returns nil. One channel failing does not
// stop the others being tried. What comes back is the first error, if any, and
// nil otherwise. The caller should only log an error and carry on booting.
//
// notify - 이벤트 하나를 설정된 채널로 밀어 낸다. 채널이 하나도 활성이
// 아니면 아무것도 안 하고 nil 을 돌려준다. 채널 중 하나가 실패해도 다른
// 채널은 계속 시도한다. 반환값은 첫 오류이고, 없으면 nil 이다. 호출부는
// 오류를 로그로만 남기고 부팅을 계속해야 한다.
func notify(event string, details map[string]string) error {
	cfg, ok := loadNotifyConfig()
	if !ok {
		return nil // no settings, skipped quietly / 설정 없음 → 조용히 스킵
	}
	payload := buildNotifyPayload(event, details)

	var firstErr error
	if cfg.WebhookURL != "" {
		if err := sendWebhook(cfg.WebhookURL, payload); err != nil {
			logf("notify: webhook: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if isEmailConfigured(cfg) {
		if err := sendEmail(cfg, event, payload); err != nil {
			logf("notify: email: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// loadNotifyConfig looks for the notification settings file along the candidate
// paths and parses it. A missing file or a parse failure gives false, and that
// is treated as "there are no settings".
//
// loadNotifyConfig - 알림 설정 파일을 후보 경로들에서 찾아 파싱한다.
// 파일이 없거나 파싱에 실패하면 false 이고, 이 조합은 "설정이 없다" 로
// 취급한다.
func loadNotifyConfig() (notifyConfig, bool) {
	var cfg notifyConfig
	for _, p := range notifyConfigPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			logf("notify: cannot parse %s: %v", p, err)
			return notifyConfig{}, false
		}
		// One active field is enough to count as real settings.
		// 하나라도 활성 필드가 있으면 진짜 설정으로 인정한다.
		if cfg.WebhookURL != "" || isEmailConfigured(cfg) {
			return cfg, true
		}
		return notifyConfig{}, false
	}
	return notifyConfig{}, false
}

// isEmailConfigured reports whether all three of the email channel's required
// fields are filled in. A partial configuration is ignored quietly: a no-op
// rather than an error.
//
// isEmailConfigured - 이메일 채널 3 필수 필드가 모두 채워졌는지. 부분 설정은
// 조용히 무시한다 (오류 대신 no-op).
func isEmailConfigured(cfg notifyConfig) bool {
	return cfg.Email.SMTP != "" && cfg.Email.From != "" && cfg.Email.To != ""
}

// buildNotifyPayload is one event's normalised set of fields. The webhook's
// JSON body and the mail body both use the same data.
//
// buildNotifyPayload - 이벤트 하나의 표준화된 필드 집합. 웹훅 JSON body 와
// 메일 본문 양쪽에서 같은 데이터를 쓴다.
func buildNotifyPayload(event string, details map[string]string) map[string]any {
	hostname, _ := os.Hostname()
	payload := map[string]any{
		"event":     event,
		"hostname":  hostname,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if ips := listIPv4Addrs(); len(ips) > 0 {
		payload["ipv4"] = strings.Join(ips, ",")
	}
	for k, v := range details {
		payload[k] = v
	}
	return payload
}

// sendWebhook POSTs the JSON, counting a 5xx as an error. A Discord-style
// response is a success, so only the status code is looked at.
//
// sendWebhook - JSON 을 POST 한다. 5xx 는 오류로 본다. Discord 같은 응답이
// 오면 성공이므로 상태코드만 본다.
func sendWebhook(url string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	client := &http.Client{Timeout: notifyHTTPTimeout}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "vibeldr-init/notify")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// sendEmail sends a text mail over STARTTLS SMTP. It is the standard flow for
// the submission port (587) on Gmail, Outlook and most others. With credentials
// present it uses PLAIN AUTH.
//
// sendEmail - STARTTLS SMTP 로 텍스트 메일을 보낸다. Gmail/Outlook 등 대부분의
// submission 포트 (587) 표준 흐름이다. 인증 정보가 있으면 PLAIN AUTH 를 쓴다.
func sendEmail(cfg notifyConfig, event string, payload map[string]any) error {
	host, _, err := net.SplitHostPort(cfg.Email.SMTP)
	if err != nil {
		return fmt.Errorf("parse smtp address: %w", err)
	}

	// Only the Dial goes through a net.Dialer with a timeout, and smtp.NewClient
	// wraps it afterwards. The default smtp.Dial has no timeout and could hold the
	// boot up.
	//
	// Dial 만 timeout 걸린 net.Dialer 로 하고, 그 뒤 smtp.NewClient 로 감싼다.
	// 기본 smtp.Dial 은 timeout 이 없어서 부팅을 붙잡을 수 있다.
	dialer := &net.Dialer{Timeout: notifySMTPTimeout}
	conn, err := dialer.Dial("tcp", cfg.Email.SMTP)
	if err != nil {
		return err
	}
	// A deadline, so the total time cannot run unbounded during the SMTP
	// session either.
	//
	// SMTP 세션 중에도 총 시간이 무제한이 되지 않도록 deadline 을 건다.
	_ = conn.SetDeadline(time.Now().Add(notifySMTPTimeout * 3))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()

	if err := c.Hello("vibeldr-init"); err != nil {
		return err
	}
	// STARTTLS. Where the server does not support it, which is rare, and
	// credentials are set, this fails rather than sending them in the clear.
	// With no credentials the mail goes out in the clear.
	//
	// STARTTLS. 서버가 지원하지 않고 (드물다) 자격증명이 설정돼 있으면 평문으로
	// 보내지 않고 실패로 남긴다. 자격증명 유출을 피하기 위해서다. 자격증명이
	// 없으면 메일은 평문으로 나간다.
	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	} else if cfg.Email.Username != "" {
		return fmt.Errorf("server does not support STARTTLS - refusing to send credentials")
	}
	if cfg.Email.Username != "" {
		auth := smtp.PlainAuth("", cfg.Email.Username, cfg.Email.Password, host)
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.Email.From); err != nil {
		return err
	}
	for _, addr := range splitAddresses(cfg.Email.To) {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(buildEmailBody(cfg, event, payload))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// splitAddresses breaks "a@x, b@y" apart, trimming whitespace and dropping
// empty entries.
//
// splitAddresses - "a@x, b@y" 를 분해한다. 공백을 없애고 빈 항목을 버린다.
func splitAddresses(csv string) []string {
	parts := strings.Split(csv, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildEmailBody is the minimum RFC 822 headers plus a text body. The subject
// carries the event name and the hostname so it is easy to filter on. There are
// no attachments.
//
// buildEmailBody - 최소 RFC 822 헤더에 텍스트 본문. Subject 에 이벤트 이름과
// hostname 을 넣어 필터하기 쉽게 한다. 첨부는 없다.
func buildEmailBody(cfg notifyConfig, event string, payload map[string]any) string {
	hostname, _ := payload["hostname"].(string)
	if hostname == "" {
		hostname = "vibeldr"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "From: %s\r\n", cfg.Email.From)
	fmt.Fprintf(&body, "To: %s\r\n", cfg.Email.To)
	fmt.Fprintf(&body, "Subject: [vibeldr] %s on %s\r\n", event, hostname)
	fmt.Fprintf(&body, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&body, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&body, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	body.WriteString("\r\n")
	// The body is key: value lines. The four that matter - event, hostname,
	// timestamp, ipv4 - go first in a fixed order; the rest come out of a map
	// and so have no order at all.
	//
	// 본문은 "key: value" 줄이다. 중요한 넷(event, hostname, timestamp, ipv4)을
	// 고정 순서로 먼저 쓰고, 나머지는 맵에서 나오므로 순서가 정해져 있지 않다.
	fmt.Fprintf(&body, "event:     %s\r\n", event)
	fmt.Fprintf(&body, "hostname:  %s\r\n", hostname)
	if ts, ok := payload["timestamp"].(string); ok {
		fmt.Fprintf(&body, "timestamp: %s\r\n", ts)
	}
	if ipv4, ok := payload["ipv4"].(string); ok {
		fmt.Fprintf(&body, "ipv4:      %s\r\n", ipv4)
	}
	for k, v := range payload {
		switch k {
		case "event", "hostname", "timestamp", "ipv4":
			continue
		}
		fmt.Fprintf(&body, "%s: %v\r\n", k, v)
	}
	return body.String()
}

// listIPv4Addrs is this machine's IPv4 addresses with loopback and link-local
// left out. It is like listIPv4 in shell_linux.go, but this one is
// cross-platform, since net.InterfaceAddrs works on every OS.
//
// listIPv4Addrs - 이 머신의 IPv4 주소 중 loopback 과 link-local 을 뺀 것들. shell_linux.go 의
// listIPv4 와 비슷하지만 이쪽은 크로스플랫폼이다 (net.InterfaceAddrs 는 모든
// OS 에서 동작).
func listIPv4Addrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}
