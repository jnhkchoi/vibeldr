package main

import (
	"strings"
	"testing"
)

// TestInstallProgressFollowsTheStages walks the stages in the order the
// pipeline prints them and checks the overall value only ever grows, moves
// inside a stage with its progress display, and ends at 1.
//
// TestInstallProgressFollowsTheStages - 파이프라인이 찍는 순서대로 단계를
// 밟으며, 전체 값이 줄지 않고, 단계 안에서는 진행률 표시로 움직이며, 끝에 1 이
// 되는지 본다.
func TestInstallProgressFollowsTheStages(t *testing.T) {
	p := newInstallProgress()
	if v, _ := p.value(); v != 0 {
		t.Fatalf("before anything, value = %v", v)
	}
	last := 0.0
	step := func(what string, f func()) {
		t.Helper()
		f()
		v, label := p.value()
		if v < last {
			t.Fatalf("%s: value went down %v -> %v", what, last, v)
		}
		if v > 1 {
			t.Fatalf("%s: value %v above 1", what, v)
		}
		if label == "" {
			t.Fatalf("%s: no label", what)
		}
		last = v
	}
	step("Fetch", func() { p.section("Fetch") })
	start := last
	step("half the download", func() { p.progress("downloading", 50, 100) })
	if last <= start {
		t.Fatalf("download progress did not move the bar: %v", last)
	}
	for _, s := range []string{"Extract", "Drivers", "Patch", "Image", "Write payload"} {
		s := s
		step(s, func() { p.section(s) })
	}
	step("flashing", func() { p.progress("flashing p3", 1, 2) })
	step("Boot menu", func() { p.section("Boot menu") })
	step("an unknown section", func() { p.section("Something new") })
	step("done", func() { p.finish() })
	if v, label := p.value(); v != 1 || !strings.Contains(label, "완료") {
		t.Fatalf("after finish: %v %q", v, label)
	}
}

// TestInstallProgressEmbeddedDSM: with the DSM kernel on the image there is no
// download; "Embedded DSM" stands where "Fetch" would.
//
// TestInstallProgressEmbeddedDSM - 이미지에 DSM 커널이 있으면 다운로드가 없고
// "Fetch" 자리에 "Embedded DSM" 이 온다.
func TestInstallProgressEmbeddedDSM(t *testing.T) {
	p := newInstallProgress()
	p.section("Embedded DSM")
	a, _ := p.value()
	p.section("Extract")
	b, _ := p.value()
	if !(b > a) {
		t.Fatalf("Extract (%v) should be past Embedded DSM (%v)", b, a)
	}
}
