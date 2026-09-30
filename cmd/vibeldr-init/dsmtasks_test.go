package main

import (
	"reflect"
	"testing"

	"vibeldr/internal/synoboot"
)

// TestParseDSMTasks: known names in order and once each, comments and blank
// lines skipped, unknown names kept apart for the log.
//
// TestParseDSMTasks - 아는 이름은 순서대로 한 번씩, 주석과 빈 줄은 건너뛰고,
// 모르는 이름은 로그용으로 따로 모은다.
func TestParseDSMTasks(t *testing.T) {
	in := "# from the rescue menu\n" + synoboot.TaskDisableTriggered + "\r\n\n  " +
		synoboot.TaskUnblockIPs + "  \nreset-everything\n" + synoboot.TaskDisableTriggered + "\n"
	known, unknown := parseDSMTasks(in)
	if want := []string{synoboot.TaskDisableTriggered, synoboot.TaskUnblockIPs}; !reflect.DeepEqual(known, want) {
		t.Errorf("known = %q, want %q", known, want)
	}
	if want := []string{"reset-everything"}; !reflect.DeepEqual(unknown, want) {
		t.Errorf("unknown = %q, want %q", unknown, want)
	}
}
