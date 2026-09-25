//go:build linux

// scemd_linux.go finds the Synology extractor (scemd) that came on the image.
//
// A .pat is a tar of zImage and rd.gz, but from DSM 7.1 on it is locked inside
// Synology's own container and the only thing that opens it is Synology's own
// scemd binary. That binary and the libraries it opens at run time are put on
// the image when the image is built (internal/image/scemd.go). So there is
// nothing to download after boot, and all this does is check that what came
// along is usable.
//
// scemd_linux.go - 이미지에 실려 온 시놀로지 자체 추출기 (scemd) 를 찾는다.
//
// .pat 은 zImage 와 rd.gz 를 담은 tar 인데 DSM 7.1 부터 시놀로지 자체
// 컨테이너로 잠겨 있고, 여는 건 시놀로지 자체 바이너리 scemd 뿐이다.
// 그 바이너리와 그것이 실행 중에 여는 라이브러리들은 이미지를 만들 때
// 함께 실린다 (internal/image/scemd.go). 그래서 부팅한 뒤에 받아 올 것은
// 없고, 여기서는 실려 온 파일이 쓸 수 있는 상태인지만 확인한다.
package main

import (
	"fmt"
	"os"

	"vibeldr/internal/catalog"
)

// scemdPath is where scemd sits on the image, and scemdLibDir is where the
// libraries it opens are put.
//
// scemdPath - 이미지에 실린 scemd 의 자리.
// scemdLibDir - 그것이 여는 라이브러리들이 놓인 자리.
const (
	scemdPath   = "/" + catalog.ScemdBinPath
	scemdLibDir = "/" + catalog.ScemdBundleDir
)

// ensureScemd returns the path of the scemd that came along, making it
// executable if it is not already.
//
// ensureScemd - 실려 온 scemd 의 경로를 돌려준다. 실행권이 없으면 붙인다.
func ensureScemd() (string, error) {
	st, err := os.Stat(scemdPath)
	if err != nil {
		return "", fmt.Errorf("scemd 가 이미지에 없음 (%s): %w", scemdPath, err)
	}
	if st.Size() == 0 {
		return "", fmt.Errorf("scemd 가 비어 있음 (%s)", scemdPath)
	}
	if st.Mode()&0o111 == 0 {
		if err := os.Chmod(scemdPath, 0o755); err != nil {
			return "", fmt.Errorf("scemd 실행권 부여: %w", err)
		}
	}
	return scemdPath, nil
}
