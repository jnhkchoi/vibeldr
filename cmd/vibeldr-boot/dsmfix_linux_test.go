//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/synoboot"
)

// TestSaveDSMFix: chosen tasks go one per line under a comment, and choosing
// none removes the file, including one that is already gone.
//
// TestSaveDSMFix - 고른 작업은 주석 아래 한 줄에 하나씩 가고, 아무것도 고르지
// 않으면 파일을 지운다. 이미 없는 파일이어도 된다.
func TestSaveDSMFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), synoboot.DSMTasksFile)
	if err := saveDSMFix(path, []string{synoboot.TaskUnblockIPs, synoboot.TaskDisableTriggered}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "#") || lines[1] != synoboot.TaskUnblockIPs || lines[2] != synoboot.TaskDisableTriggered {
		t.Fatalf("file = %q", raw)
	}
	for i := 0; i < 2; i++ {
		if err := saveDSMFix(path, nil); err != nil {
			t.Fatalf("clear %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
}
