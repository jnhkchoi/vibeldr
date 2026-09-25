package pat

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Format is how a .pat is packaged.
// Format - .pat 이 어떻게 포장돼 있는지.
type Format int

const (
	FormatUnknown Format = iota
	// FormatTar is a plain uncompressed tar.
	// FormatTar - 압축 없는 평문 tar.
	FormatTar
	// FormatTarGz is a gzip-compressed tar.
	// FormatTarGz - gzip 으로 압축된 tar.
	FormatTarGz
	// FormatEncrypted is Synology's own encrypted container, which needs the
	// vendor's syno_extract_system_patch to open.
	//
	// FormatEncrypted - 시놀로지 자체 암호화 컨테이너. 여는 데 벤더의
	// syno_extract_system_patch 가 필요하다.
	FormatEncrypted
)

func (f Format) String() string {
	switch f {
	case FormatTar:
		return "tar"
	case FormatTarGz:
		return "tar.gz"
	case FormatEncrypted:
		return "encrypted"
	default:
		return "unknown"
	}
}

// ErrEncrypted is returned when a .pat needs Synology's own extractor.
//
// The Windows side of vibeldr does not handle that case itself. The actual
// decryption is done by the vibeldr-boot TUI on the booted machine, using the
// scemd it took out of a seed DSM.
//
// ErrEncrypted - .pat 이 시놀로지 자체 추출기를 필요로 할 때 반환된다.
//
// Windows 쪽 vibeldr 는 이 케이스를 직접 다루지 않는다. 실제 복호화는 부팅된
// 머신의 vibeldr-boot TUI 가 seed DSM 에서 뽑은 scemd 를 통해 처리한다.
var ErrEncrypted = errors.New("암호화된 .pat 은 vibeldr-boot TUI 에서만 처리됨 (VM 부팅 후 자동)")

// DetectFormat looks at the first block of a .pat.
//
// It checks the real container magic bytes rather than guessing from byte
// values: a gzip header means tar.gz and "ustar" at offset 257 means a plain
// tar. Anything else is taken to be Synology's encrypted container.
// FormatUnknown comes back only together with a read error.
//
// DetectFormat - .pat 의 첫 블록을 살펴본다.
//
// 바이트 값 휴리스틱이 아니라 실제 컨테이너 magic 바이트를 검사한다. gzip
// 헤더면 tar.gz, offset 257 에 "ustar" 가 있으면 평문 tar 다. 그 밖의 것은
// 전부 시놀로지 암호화 컨테이너로 본다. FormatUnknown 은 읽기 에러와 함께만
// 돌아온다.
func DetectFormat(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return FormatUnknown, err
	}
	header = header[:n]

	if len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b {
		return FormatTarGz, nil
	}
	// A POSIX tar carries "ustar" at offset 257.
	// POSIX tar 은 offset 257 에 "ustar" 를 갖고 있다.
	if len(header) >= 262 && bytes.HasPrefix(header[257:262], []byte("ustar")) {
		return FormatTar, nil
	}
	return FormatEncrypted, nil
}

// WantedFiles are the names inside a .pat that a loader build needs.
// WantedFiles - 로더 빌드에 필요한 .pat 안 파일 이름들.
var WantedFiles = []string{
	"zImage",
	"rd.gz",
	"VERSION",
	"GRUB_VER",
	"grub_cksum.syno",
}

// ExtractResult records what came out of a .pat.
// ExtractResult - .pat 에서 나온 것들의 기록.
type ExtractResult struct {
	Format  Format
	Files   map[string]string // member name -> written path / 멤버 이름 -> 쓴 경로
	Hashes  map[string]string // member name -> sha256 / 멤버 이름 -> sha256
	Missing []string
}

// Extract pulls WantedFiles out of a .pat into destDir.
//
// Matching is on the base name. Synology's own archives sometimes prefix names
// with "./", which would break an exact-name match.
//
// Extract - .pat 에서 WantedFiles 를 destDir 로 뽑는다.
//
// 매칭은 basename 으로 한다. 시놀로지 자체 아카이브가 이름 앞에 "./" 를 붙이는
// 관례가 있어 exact-name 매칭이 어긋나는 걸 피한다.
func Extract(patPath, destDir string) (*ExtractResult, error) {
	format, err := DetectFormat(patPath)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", patPath, err)
	}
	if format == FormatEncrypted {
		// No pure-Go decryption. Synology has replaced this container several
		// times (Salted__/OpenSSL, __PATFILE__, 35 AD BE EF and others), and
		// chasing each one by reverse engineering only breaks again at the
		// next release. Instead the vibeldr-boot TUI takes Synology's own
		// extractor (scemd) out of a seed DSM and handles every format with it.
		//
		// 순수 Go 복호화는 하지 않는다. 시놀로지는 이 컨테이너를 여러 번
		// 갈아엎었고 (Salted__/OpenSSL, __PATFILE__, 35 AD BE EF …) 리버싱으로
		// 뒤쫓아 봐야 다음 릴리스에서 또 깨진다. 대신 vibeldr-boot TUI 가
		// seed DSM 에서 시놀로지 자체 추출기 (scemd) 를 뽑아 두고 그걸로 모든
		// 포맷을 일괄 처리한다.
		return nil, fmt.Errorf("%s: %w", filepath.Base(patPath), ErrEncrypted)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", destDir, err)
	}

	f, err := os.Open(patPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var src io.Reader = f
	if format == FormatTarGz {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gunzip %s: %w", patPath, err)
		}
		defer gz.Close()
		src = gz
	}

	want := make(map[string]bool, len(WantedFiles))
	for _, name := range WantedFiles {
		want[name] = true
	}

	result := &ExtractResult{
		Format: format,
		Files:  make(map[string]string),
		Hashes: make(map[string]string),
	}

	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", patPath, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Base(hdr.Name)
		if !want[name] {
			continue
		}

		outPath := filepath.Join(destDir, name)
		sum, err := writeAndHash(tr, outPath)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", name, err)
		}
		result.Files[name] = outPath
		result.Hashes[name] = sum
	}

	for _, name := range WantedFiles {
		if _, ok := result.Files[name]; !ok {
			result.Missing = append(result.Missing, name)
		}
	}
	sort.Strings(result.Missing)

	// zImage and rd.gz are the two the build genuinely cannot proceed without.
	// zImage 와 rd.gz 둘은 없으면 빌드가 정말로 진행되지 않는다.
	for _, essential := range []string{"zImage", "rd.gz"} {
		if _, ok := result.Files[essential]; !ok {
			return result, fmt.Errorf("%s does not contain %s - is it really a DSM firmware archive?",
				filepath.Base(patPath), essential)
		}
	}
	return result, nil
}

// writeAndHash streams r into path and returns the content's sha256. Hashing
// during the copy avoids a second full read of a file that can be 100 MB.
//
// writeAndHash - r 을 path 로 흘려 쓰면서 내용의 sha256 을 돌려준다. 복사
// 중에 해시하면 100 MB 짜리 파일을 두 번 읽지 않아도 된다.
func writeAndHash(r io.Reader, dest string) (string, error) {
	out, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), r); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VersionInfo is the parsed VERSION file from inside a .pat.
// VersionInfo - .pat 안의 VERSION 파일 파싱 결과.
type VersionInfo struct {
	Major      string
	Minor      string
	Micro      string
	Build      string
	SmallFix   string
	ProductVer string // "7.4"
	Full       string // "7.4.1-90080"
}

// ParseVersionFile reads the shell-style KEY="value" VERSION file that ships in
// every .pat. Verifying this against the requested version is what catches a
// mislabelled URL before a wrong kernel is patched.
//
// ParseVersionFile - 모든 .pat 에 들어 있는 셸 변수 형식 KEY="value" VERSION
// 파일을 읽는다. 이 값을 요청한 버전과 대조하는 것이, 잘못된 커널을 패치하기
// 전에 잘못 붙은 URL 을 잡아내는 방법이다.
func ParseVersionFile(path string) (*VersionInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		kv[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), `"`)
	}

	v := &VersionInfo{
		Major:    kv["majorversion"],
		Minor:    kv["minorversion"],
		Micro:    kv["micro"],
		Build:    kv["buildnumber"],
		SmallFix: kv["smallfixnumber"],
	}
	if v.Major == "" || v.Minor == "" || v.Build == "" {
		return nil, fmt.Errorf("VERSION is missing majorversion/minorversion/buildnumber")
	}
	v.ProductVer = v.Major + "." + v.Minor
	if pv := kv["productversion"]; pv != "" {
		v.Full = pv + "-" + v.Build
	} else if v.Micro != "" {
		v.Full = v.ProductVer + "." + v.Micro + "-" + v.Build
	} else {
		v.Full = v.ProductVer + "-" + v.Build
	}
	return v, nil
}
