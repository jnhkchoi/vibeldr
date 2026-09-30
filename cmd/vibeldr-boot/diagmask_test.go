package main

import (
	"strings"
	"testing"
)

// The serial number, the MAC and card-order items and the notification values
// are blanked; everything else, a bay list of PCI addresses included, stays.
//
// 시리얼, MAC·카드 순서 항목, 알림 값은 지우고, 나머지는 PCI 주소로 된 베이
// 목록까지 그대로 둔다.
func TestMaskConfig(t *testing.T) {
	in := strings.Join([]string{
		"model: DS918+",
		"identity:",
		`    serial: "1910PDN003265"`,
		"    macs:",
		"        - bc:24:11:00:19:51",
		"        - bc:24:11:00:19:52",
		"    nic_order:",
		"        - mac:bc2411001951",
		"    nic_count: 2",
		"storage:",
		"    bay_order:",
		"        - 0000:00:1f.2",
		"notify:",
		"    webhook_url: https://hook.example/secret",
		"    email:",
		"        password: hunter2",
		"        to: \"\"",
		"other:",
		"    macs: [aa:bb:cc:dd:ee:ff]",
		"    empty: []",
	}, "\n")
	got := string(maskConfig([]byte(in)))
	for _, gone := range []string{"1910PDN003265", "bc:24:11", "bc2411001951", "secret", "hunter2", "aa:bb:cc"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s left in:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"model: DS918+", "nic_count: 2", "- 0000:00:1f.2", `to: ""`, "empty: []"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s lost:\n%s", kept, got)
		}
	}
}
