//go:build linux

package main

import (
	"strings"
	"testing"
)

// The rule runs the agent with the interface's modalias and path, for USB
// interfaces only.
//
// 규칙은 USB 인터페이스에만, 그 modalias 와 경로를 주어 에이전트를 부른다.
func TestHotplugRule(t *testing.T) {
	got := hotplugRuleText()
	for _, want := range []string{`SUBSYSTEM=="usb"`, `ENV{DEVTYPE}=="usb_interface"`, `RUN+="` + dsmAgentName + ` -hotplug $env{MODALIAS} $devpath"`} {
		if !strings.Contains(got, want) {
			t.Errorf("rule lacks %s:\n%s", want, got)
		}
	}
}
