package main

import (
	"strings"
	"testing"
)

// The rule marks the disk and its partitions, and only a plain kernel disk
// name is accepted for it.
//
// 규칙은 디스크와 그 파티션을 표시하고, 평범한 커널 디스크 이름만 받아들인다.
func TestSynobootRule(t *testing.T) {
	got := synobootRuleText("sdq")
	if !strings.Contains(got, `SUBSYSTEM=="block", KERNELS=="sdq", ENV{SYNO_DEV_DISKPORTTYPE}="SYNOBOOT"`) {
		t.Fatalf("rule:\n%s", got)
	}
	for name, ok := range map[string]bool{"sdq": true, "sdu": true, "usb1": true, "nvme0n1": true, "": false, "sd q": false, `sdq", RUN+="x`: false, "../sda": false} {
		if diskName.MatchString(name) != ok {
			t.Errorf("diskName(%q) = %v, want %v", name, !ok, ok)
		}
	}
}
