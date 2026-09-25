//go:build linux

// scemd_run_linux.go calls the cached scemd to unpack an encrypted .pat.
//
// scemd is Synology's own binary and its interface is what was observed:
//
//	scemd <pat path> <output directory>
//
// After it runs, the standard members - zImage, rd.gz, VERSION and so on -
// appear in the output directory.
//
// scemd_run_linux.go - 캐시된 scemd 를 호출해 암호화된 .pat 을 풀어 놓는다.
//
// scemd 는 시놀로지 자체 바이너리이고, 인터페이스는 위 영문처럼 관측한
// 그대로다. 실행 후 출력 디렉터리에 zImage / rd.gz / VERSION 등 표준
// 멤버가 나타난다.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// decryptPatWithScemd unpacks patPath into outDir using the scemd at scemdPath.
//
// It can take a good while in the boot environment, so it waits up to ten
// minutes. On any exit code other than 0 the stderr and stdout go into the error
// message verbatim, for the TUI above to show. If neither zImage nor rd.gz ends
// up in the output directory it is treated as a failure.
//
// decryptPatWithScemd - scemdPath 로 patPath 를 outDir 에 풀어 놓는다.
//
// 부트 환경에서 상당 시간이 걸릴 수 있어 최대 10 분 기다린다. exit code 가
// 0 이 아니면 stderr/stdout 을 그대로 에러 메시지에 담아 상위 TUI 가
// 표시하게 한다. 출력 디렉터리에 zImage 또는 rd.gz 이 하나도 없으면 실패로
// 처리한다.
func decryptPatWithScemd(scemdPath, patPath, outDir string) error {
	if scemdPath == "" {
		return errors.New("scemd 경로 미지정")
	}
	if _, err := os.Stat(scemdPath); err != nil {
		return fmt.Errorf("scemd 없음 (%s): %w", scemdPath, err)
	}
	if _, err := os.Stat(patPath); err != nil {
		return fmt.Errorf("pat 없음 (%s): %w", patPath, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("out dir 생성: %w", err)
	}

	cmd := exec.Command(scemdPath, patPath, outDir)
	cmd.Env = append(os.Environ(),
		// scemd looks its crypto libraries up by name at run time. This
		// points it at where the image put them.
		//
		// scemd 는 크립토 라이브러리를 실행 중에 이름으로 찾아 연다.
		// 이미지가 그 라이브러리들을 실어 둔 자리를 알려 준다.
		"LD_LIBRARY_PATH="+scemdLibDir,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A hard ten-minute limit. This is a boot, so leaving the user waiting
	// forever is not an option.
	//
	// 10 분 하드 리밋. 부트 시나리오라 사용자가 무한정 기다리는 건 논외다.
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("scemd 실행 시작: %w", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("scemd 실패 (exit %s):\n  stdout: %s\n  stderr: %s",
				err, trimForLog(stdout.String()), trimForLog(stderr.String()))
		}
	case <-time.After(10 * time.Minute):
		_ = cmd.Process.Kill()
		return errors.New("scemd 실행이 10 분을 초과 (hang 추정)")
	}

	// The least possible check: one of the standard members has to be there
	// for anything to actually have come out. It can exit 0 and produce
	// nothing, so what it said is shown alongside.
	//
	// 최소한의 검증: 표준 멤버 중 하나가 나와야 실제로 뭔가 뽑힌 것이다.
	// 종료 코드가 0 이어도 아무것도 안 내놓는 경우가 있어, 그때 무엇을
	// 말했는지 함께 보여 준다.
	if !containsAny(outDir, []string{"zImage", "rd.gz", "VERSION"}) {
		return fmt.Errorf("추출기가 종료 코드 0 으로 끝났으나 %s 에 zImage/rd.gz/VERSION 이 하나도 없음:\n  stdout: %s\n  stderr: %s",
			outDir, trimForLog(stdout.String()), trimForLog(stderr.String()))
	}
	return nil
}

// containsAny reports whether dir holds a regular file named as one of wants.
// containsAny - dir 안에 이름이 wants 중 하나인 일반 파일이 있는지.
func containsAny(dir string, wants []string) bool {
	for _, name := range wants {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// trimForLog cuts very long output down for the log, keeping 1KB at each end.
// trimForLog - 로그 표시용으로 매우 긴 출력을 잘라 낸다 (앞 뒤 각 1KB).
func trimForLog(s string) string {
	const limit = 1024
	if len(s) <= 2*limit {
		return s
	}
	return s[:limit] + "\n...(생략)...\n" + s[len(s)-limit:]
}
