# releases.json - 모델별 DSM 릴리스 카탈로그

`loader.yaml` 에 URL 과 MD5 를 손으로 타이핑하는 흐름은 오타 하나에
몇 시간을 날릴 수 있다. 이 파일이 "SA6400 의 7.4.1-90080" 같이 짧은
좌표만 있어도 툴이 어디서 받고 무엇으로 검증할지 알게 해 준다.

## 구조

```
{
  "<model>": {
    "<full-version>": {
      "url":    "<https URL to the .pat>",
      "md5":    "<32 hex digits, or \"\" if unknown yet>",
      "kernel": "<matches platforms.json kernels entry>"
    }
  }
}
```

- `<model>` 은 `platforms.json` 의 어떤 플랫폼에도 등록되어 있어야 한다.
  아니면 `releases_test.go` 가 걸러 낸다.
- `<full-version>` 은 `X.Y[.Z]-BUILD` 형식이어야 한다
  (예: `7.4.1-90080`, `7.2-72806`). `internal/config` 의 `dsmVersionPattern`
  과 같은 규약.

## 현재 상태

- 총 엔트리 수: 287 (95 개 모델)
- 실제 MD5 채워진 엔트리: 287
- MD5 가 비어 있는 엔트리: 0

카탈로그의 96 개 모델 중 95 개에 릴리스 엔트리가 있다. `PAS7700` 만 CDN 에
`.pat` 이 공개돼 있지 않아 비어 있고, 이 모델은 부트 TUI 의 DSM 메뉴에서
`t`/`u`/`m` 으로 버전·URL·MD5 를 직접 넣어야 한다.

실린 빌드는 플랫폼이 지원하는 productver 에 해당하는 것만이다.

| 빌드 | DSM 버전 |
| ---- | -------- |
| 90080 | 7.4.1 |
| 86009 | 7.3.2 |
| 72806 | 7.2.2 |
| 42962 | 7.1.1 |

모든 해시는 Synology CDN 의 `.pat.md5` 사이드카 파일에서 직접 가져왔다.
빈 문자열은 "검증 없이 다운로드" 로 처리되므로 새 엔트리는 md5 를
못 구했더라도 URL 만으로 사용 가능하다.

## 새 항목 추가하기

1. Synology 릴리스 노트에서 정확한 빌드 번호를 확인한다
   (`https://www.synology.com/api/support/findDownloadInfo?lang=en-global&product=<MODEL>`
   가 사람이 읽기 좋은 JSON 을 돌려준다).
2. `platforms.json` 에 그 모델이 있는지, 해당 `X.Y` 커널이 등록되어
   있는지 먼저 확인한다. 없다면 `platforms.json` 부터 채워야 한다.
3. 아래의 "MD5 채우는 법" 을 따른다.
4. `go test ./internal/catalog/...` 로 카탈로그 규약 검사를 통과시킨다.

## MD5 채우는 법

Synology CDN 은 각 `.pat` 옆에 32 byte 짜리 사이드카 `.pat.md5` 를
같이 올려 두고, 이게 사실상 유일하게 스크립트로 긁을 수 있는 공식
해시 소스다. 릴리스 노트 페이지와 다운로드 센터는 SPA 라 크롤링이
잘 안 되고, `archive.synology.com` 의 디렉토리 인덱스에는 해시가
없다.

```
# 해시 파일 (32 바이트)
curl -s 'https://global.synologydownload.com/download/DSM/release/7.4.1/90080/DSM_SA6400_90080.pat.md5'

# 필요하면 직접 검증
curl -sSL 'https://global.synologydownload.com/download/DSM/release/7.4.1/90080/DSM_SA6400_90080.pat' | md5sum
```

## `+` 문자가 들어간 모델 이름

`DS923+`, `DS3622xs+` 처럼 이름에 `+` 가 있는 모델은 CDN 파일명에도
그대로 `+` 가 들어간다 (URL 인코딩된 `%2B` 를 써도 되지만 원시 `+`
로도 서빙됨). 카탈로그는 가독성 때문에 원시 `+` 를 쓴다.

## 알려진 빌드 → DSM 버전 매핑

빌드 번호는 DSM 마이너 버전과 1:1 이 아니라 헷갈리기 쉽다.
잘 알려진 최신 빌드는 아래와 같다 (2026 년 9 월 기준):

| 빌드 | DSM 버전 |
| ---- | -------- |
| 90080 | 7.4.1 |
| 90075 | 7.4 |
| 86009 | 7.3.2 |
| 86003 | 7.3.1 |
| 81180 | 7.3 |
| 72806 | 7.2.2 |
| 69057 | 7.2.1 |
| 64570 | 7.2 |
| 42962 | 7.1.1 |
