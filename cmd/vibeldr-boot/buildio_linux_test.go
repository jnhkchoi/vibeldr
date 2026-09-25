//go:build linux

// buildio_linux_test.go checks that the points where the build pipeline waits
// for the user only go through the channel suited to the kind of screen.
//
// buildio_linux_test.go - 빌드 파이프라인이 사용자를 기다리는 지점이
// 화면 종류에 맞는 통로로만 가는지 확인한다.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordIO is a buildIO that records what was asked. Like the graphical screen,
// it does not wait and answers empty.
//
// recordIO - 무엇을 물어봤는지 기록하는 buildIO. 그래픽 화면처럼 기다리지
// 않고 빈 답을 준다.
type recordIO struct {
	prompts []string
	pauses  []string
}

func (r *recordIO) Pause(msg string) { r.pauses = append(r.pauses, msg) }
func (r *recordIO) Prompt(label string) string {
	r.prompts = append(r.prompts, label)
	return ""
}

// TestGrubRewriteAsksThroughBuildIO checks that the grub rewrite step asks
// through buildIO instead of reading standard input directly.
//
// Reading stdin directly wedges the graphical installer: the screen has
// nowhere to type an answer, so nobody ever presses a key and the install
// stops there with no indication why.
//
// TestGrubRewriteAsksThroughBuildIO - grub 재작성 단계가 표준 입력을 직접
// 읽지 않고 buildIO 로 묻는지 확인한다.
//
// 직접 읽으면 그래픽 설치 화면이 그 자리에서 멈춘다. 화면에는 답할 자리가
// 없어 아무도 키를 누르지 못하고, 멈춘 이유도 화면에 안 나온다.
func TestGrubRewriteAsksThroughBuildIO(t *testing.T) {
	src, err := os.ReadFile("tui_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(string(src), "rewriteGrubForDSM")
	if body == "" {
		t.Fatal("rewriteGrubForDSM 을 찾지 못함")
	}
	if strings.Contains(body, "prompt(") && !strings.Contains(body, "bio.Prompt(") {
		t.Error("rewriteGrubForDSM 이 표준 입력을 직접 읽는다 - bio.Prompt 를 써야 한다")
	}
	if strings.Contains(body, "\tpause(") {
		t.Error("rewriteGrubForDSM 이 pause() 로 기다린다 - bio.Pause 를 써야 한다")
	}
}

// TestBuildHelpersDoNotBlockOnStdin checks in one go that none of the helpers
// buildFlow calls reads standard input directly.
//
// TestBuildHelpersDoNotBlockOnStdin - buildFlow 가 부르는 도우미들이
// 표준 입력을 직접 읽지 않는지 한꺼번에 본다.
func TestBuildHelpersDoNotBlockOnStdin(t *testing.T) {
	src, err := os.ReadFile("tui_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	// The functions buildFlow calls, the ones that run during an install.
	// buildFlow 가 부르는, 설치 중에 도는 함수들.
	helpers := []string{
		"rewriteGrubForDSM",
		"resolveIdentityForBuild",
		"fetchDriverPack",
		"writeDriverPack",
		"patchZImageTUI",
		"collectExtractedDSM",
	}
	for _, name := range helpers {
		body := funcBody(text, name)
		if body == "" {
			continue
		}
		for _, bad := range []string{"= prompt(", " prompt(", "\tpause("} {
			if strings.Contains(body, bad) {
				t.Errorf("%s 가 %q 로 표준 입력을 기다린다 - 그래픽 화면이 멈춘다", name, strings.TrimSpace(bad))
			}
		}
	}
}

// funcBody cuts one function's body out of the source, from the opening line to
// the closing brace at column 0.
//
// funcBody - 소스에서 함수 하나의 본문을 잘라낸다. 여는 줄부터 열 0 의
// 닫는 중괄호까지.
func funcBody(src, name string) string {
	start := strings.Index(src, "\nfunc "+name+"(")
	if start < 0 {
		return ""
	}
	rest := src[start+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// TestBuildIOFileExists: whether the source this test reads is a real package
// file.
//
// TestBuildIOFileExists - 이 테스트가 보는 소스가 실제 패키지 파일인지 본다.
func TestBuildIOFileExists(t *testing.T) {
	if _, err := os.Stat(filepath.Join(".", "tui_linux.go")); err != nil {
		t.Fatalf("tui_linux.go 가 없음: %v", err)
	}
}
