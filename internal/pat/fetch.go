// Package pat downloads and extracts Synology .pat firmware archives.
// Package pat - 시놀로지 .pat 펌웨어 아카이브 다운로드/추출.
package pat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EmptyMD5 is the sentinel a config uses to mean "do not verify".
// EmptyMD5 - 설정이 "검증하지 않음" 을 뜻할 때 쓰는 표식 값.
const EmptyMD5 = "00000000000000000000000000000000"

// Progress reports download progress. total is -1 when the server did not send
// a Content-Length.
//
// Progress - 다운로드 진행 상황 리포트. 서버가 Content-Length 를 안 주면
// total 은 -1 이다.
type Progress func(done, total int64)

// Fetcher is a .pat downloader that can resume.
// Fetcher - resume 지원되는 .pat 다운로더.
type Fetcher struct {
	Client *http.Client
	// OnProgress is called at most a few times per second.
	// OnProgress - 초당 몇 번을 넘지 않게 호출된다.
	OnProgress Progress
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		Client: &http.Client{
			// A .pat is several hundred megabytes; only the handshake and the
			// idle gaps need a deadline, not the whole transfer.
			//
			// .pat 은 수백 MB 다. 기한을 둘 곳은 핸드셰이크와 유휴 구간이지
			// 전송 전체가 아니다.
			Timeout: 0,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   30 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

// Fetch downloads url to dest, resuming a partial download when the server
// supports it. A file that is already complete and passes verification is left
// alone. TLS is always verified and the content is checked against wantMD5
// (unless it is empty or EmptyMD5), because the payload is a kernel that is
// about to run.
//
// Fetch - url 을 dest 로 다운로드한다. 서버가 지원하면 partial 다운로드를
// resume 한다. 완성돼 있고 검증까지 통과한 파일은 그대로 둔다. TLS 는 항상
// 검증하고 내용은 wantMD5 와 대조한다 (wantMD5 가 비었거나 EmptyMD5 면 안 한다).
// 페이로드가 곧 실행될 커널이기 때문이다.
func (f *Fetcher) Fetch(ctx context.Context, url, dest, wantMD5 string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	// Already downloaded and still good? Nothing to do.
	// 이미 받았고 아직 멀쩡하면 할 일이 없다.
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		if !needsVerify(wantMD5) {
			return nil
		}
		sum, err := FileMD5(dest)
		if err == nil && strings.EqualFold(sum, wantMD5) {
			return nil
		}
		// Cached file is wrong; start over rather than resuming onto bad bytes.
		// 캐시 파일이 틀렸다. 잘못된 바이트 위에 이어 붙이지 않고 처음부터 받는다.
		if err := os.Remove(dest); err != nil {
			return fmt.Errorf("remove stale cache %s: %w", dest, err)
		}
	}

	part := dest + ".part"
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored our Range header, so the partial file is useless.
		// 서버가 Range 헤더를 무시했으므로 받다 만 파일은 쓸모가 없다.
		offset = 0
		flags |= os.O_TRUNC
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	default:
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	total := resp.ContentLength
	if total >= 0 {
		total += offset
	}

	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", part, err)
	}

	done := offset
	lastReport := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", part, werr)
			}
			done += int64(n)
			if f.OnProgress != nil && time.Since(lastReport) > 200*time.Millisecond {
				f.OnProgress(done, total)
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			// The .part file is kept so the next run can resume from here.
			// .part 파일은 남겨 둔다. 다음 실행이 여기서 이어받는다.
			return fmt.Errorf("download %s: %w", url, readErr)
		}
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", part, err)
	}
	if f.OnProgress != nil {
		f.OnProgress(done, total)
	}

	if total > 0 && done != total {
		return fmt.Errorf("download %s: got %d bytes, expected %d", url, done, total)
	}

	if needsVerify(wantMD5) {
		sum, err := FileMD5(part)
		if err != nil {
			return fmt.Errorf("checksum %s: %w", part, err)
		}
		if !strings.EqualFold(sum, wantMD5) {
			// Do not leave a file that looks usable but is not.
			// 쓸 수 있어 보이지만 실제로는 아닌 파일을 남기지 않는다.
			os.Remove(part)
			return fmt.Errorf("checksum mismatch for %s:\n  expected %s\n  got      %s",
				filepath.Base(dest), wantMD5, sum)
		}
	}

	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("finalise %s: %w", dest, err)
	}
	return nil
}

func needsVerify(md5sum string) bool {
	return md5sum != "" && !strings.EqualFold(md5sum, EmptyMD5)
}

// FileMD5 is a file's MD5 in lower-case hex. Synology publishes an MD5 for each
// .pat, so that is what verification uses. MD5 is not a security-grade hash,
// but TLS carries the integrity of the transfer and the MD5 only confirms that
// the download is complete and intact.
//
// FileMD5 - 파일의 소문자 hex MD5. 시놀로지가 pat 에 대해 MD5 를 공개하므로
// 그걸 검증에 쓴다. MD5 는 보안 등급 해시가 아니지만, 전송 무결성은 TLS 가
// 담당하고 MD5 는 다운로드 완결성 확인 용도다.
func FileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
