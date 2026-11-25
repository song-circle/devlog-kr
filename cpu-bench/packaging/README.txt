cpu-bench {VERSION}
====================

내 PC가 제 성능을 내는지 알려 주는 Windows용 무료 진단형 CPU 벤치마크입니다.
점수와 함께, 점수가 낮다면 왜 그런지(전원 설정, 메모리 채널, XMP, 백그라운드 프로그램, 쓰로틀링)를 알려 줍니다.

실행 방법
---------
1. 이 폴더를 원하는 곳에 압축 해제합니다. 설치는 필요 없습니다.
2. cpu-bench.exe를 실행합니다.
3. "Windows의 PC 보호" 창이 뜨면 [추가 정보] → [실행]을 누릅니다.
   아직 코드 서명 인증서가 없는 초기 버전이라 나오는 경고입니다.
   받은 파일이 원본인지는 아래 "파일 확인"으로 확인할 수 있습니다.

필요 환경: Windows 10/11 64비트(x64). .NET 등 따로 설치할 것은 없습니다.

들어 있는 파일
--------------
cpu-bench.exe          화면 앱
cpu-bench-runner.exe   측정 프로그램 (앱이 실행합니다. 명령줄에서 직접 써도 됩니다)
bench_int.dll          정수 연산 부하 모듈
bench_mem.dll          메모리 부하 모듈
bench_fp.dll           부동소수점(실수 계산) 부하 모듈
bench_mt.dll           동기화(멀티코어) 부하 모듈
LICENSE.txt            MIT 라이선스

이 프로그램이 하는 일과 하지 않는 일
-----------------------------------
- 진단은 읽기만 합니다. Windows 설정을 바꾸거나 다른 프로그램을 종료하지 않습니다.
- 측정 결과는 이 PC에만 저장합니다: %LOCALAPPDATA%\cpu-bench\results
- PC 측정 결과나 PC 정보는 어디에도 보내지 않습니다.
- 예외: [인터넷 속도]를 누르고 동의하면 M-Lab(인터넷 연구용 공개 플랫폼) 서버와 속도를 잽니다.
  이때 속도 결과와 공인 IP 주소가 M-Lab의 공개 데이터로 남습니다. 누르지 않으면 통신하지 않습니다.
- 켤 때 GitHub에 최신 버전 번호만 물어보고, 새 버전이 있으면 화면 위에 알려 줍니다.
  (화면 아래 "시작할 때 새 버전 확인"을 끄면 묻지 않습니다. 자동으로 내려받거나 바꾸지 않습니다.)

지우는 방법
-----------
이 폴더와 %LOCALAPPDATA%\cpu-bench 폴더를 지우면 끝입니다.

파일 확인 (선택)
----------------
받은 zip 파일이 배포한 원본과 같은지 PowerShell에서 확인할 수 있습니다:
  Get-FileHash .\cpu-bench-{VERSION}-win-x64.zip -Algorithm SHA256
나온 값이 다운로드 페이지에 적힌 SHA-256 값과 같으면 원본입니다.

새 버전 / 문의
--------------
- 새 버전: https://github.com/song-circle/devlog-kr/releases
- 버그 신고·기능 요청: https://github.com/song-circle/devlog-kr/issues
- 개발 과정 영상: https://www.youtube.com/@buglog_kr

MIT 라이선스로 공개된 무료 프로그램입니다. 코드: https://github.com/song-circle/devlog-kr
