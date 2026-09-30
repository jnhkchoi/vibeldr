//go:build linux

// rescue_linux.go holds the rescue shell's commands that look at the machine:
// mounts, block devices, network, logs, and a real shell when the image has
// one. The shell itself is rescueLoop in tui_linux.go.
//
// rescue_linux.go - 복구 셸에서 기계를 들여다보는 명령들: 마운트, 블록 장치,
// 네트워크, 로그, 이미지에 있으면 진짜 셸. 셸 자체는 tui_linux.go 의
// rescueLoop 다.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// cmdMounts dumps /proc/mounts as it is: which filesystem is mounted where and
// with what.
//
// cmdMounts - /proc/mounts 를 그대로 덤프한다. 어느 fs 가 어디에 뭘로 붙었는지.
func cmdMounts() {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		fmt.Println("mount:", err)
		return
	}
	fmt.Print(string(b))
}

// cmdLsblk imitates lsblk by printing /proc/partitions as it is. The real lsblk
// walks sysfs, but coming out of one file is tidier here; cmdParts is the
// separate view that does walk sysfs.
//
// cmdLsblk - lsblk 흉내. /proc/partitions 를 그대로 찍는다. 실제 lsblk 는
// sysfs 를 훑는 게 정석이지만 파일 하나로 나오는 이쪽이 더 간결하다.
// cmdParts 가 sysfs 를 훑는 별도 view 다.
func cmdLsblk() {
	b, err := os.ReadFile("/proc/partitions")
	if err != nil {
		fmt.Println("lsblk:", err)
		return
	}
	fmt.Print(string(b))
}

// cmdNet summarises the interfaces and the routes, reusing interfaces() from
// net_linux.go.
//
// cmdNet - 인터페이스와 route 요약. net_linux.go 의 interfaces() 를 재사용한다.
func cmdNet() {
	// Each interface's mac and carrier, exactly as cmdIP shows them.
	// 인터페이스 각각의 mac / carrier - cmdIP 와 같다.
	cmdIP()
	fmt.Println()
	fmt.Println("routes (/proc/net/route):")
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		fmt.Println("  route:", err)
	} else {
		fmt.Print(string(b))
	}
	// The interface names once more; interfaces() comes from net_linux.go.
	// 인터페이스 이름만 다시 한 번 (interfaces() 는 net_linux.go 것).
	names, err := interfaces()
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println("interfaces:")
	for _, n := range names {
		fmt.Println("  " + n)
	}
}

// cmdJournal lists the files under /var/log with their sizes, and points at the
// existing `logs <path>` for tailing one of them.
//
// cmdJournal - /var/log 아래 파일 목록 (크기 포함). 각 파일의 tail 은 기존
// `logs <path>` 를 쓰도록 안내한다.
func cmdJournal() {
	entries, err := os.ReadDir("/var/log")
	if err != nil {
		fmt.Println("journal:", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("(empty)")
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		fmt.Printf("  %-40s %10d bytes\n", e.Name()+suffix, info.Size())
	}
	fmt.Println()
	fmt.Println("tail a file: `logs /var/log/<name>`")
}

// cmdShell spawns the first shell it finds, trying bash, sh and then ash, with
// stdio passed straight through. When the shell exits it comes back to the
// rescue REPL.
//
// cmdShell - bash / sh / ash 순서로 처음 발견되는 shell 을 띄운다. stdio 는
// 그대로 이어 붙인다. shell 이 끝나면 rescue REPL 로 돌아온다.
func cmdShell() {
	for _, path := range []string{"/bin/bash", "/bin/sh", "/bin/ash", "/usr/bin/bash"} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		c := exec.Command(path)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		fmt.Println("[spawning " + path + " - type exit to return]")
		_ = c.Run()
		return
	}
	fmt.Println("no shell: none of /bin/bash, /bin/sh, /bin/ash exists.")
}

// isMounted reports whether target appears as a mount point in /proc/mounts.
// isMounted - /proc/mounts 에 target 이 마운트 대상으로 존재하는지.
func isMounted(target string) bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == target {
			return true
		}
	}
	return false
}
