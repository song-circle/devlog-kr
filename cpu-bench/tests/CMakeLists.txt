add_executable(int_kernels_test int_kernels_test.cpp)
target_link_libraries(int_kernels_test PRIVATE int_kernels bench_options)
add_test(NAME int_kernels COMMAND int_kernels_test)

# 커널 + 모듈 DLL(import 라이브러리로 링크)의 checksum을 Python 독립 구현과 비교
add_executable(mem_kernels_test mem_kernels_test.cpp)
target_link_libraries(mem_kernels_test PRIVATE mem_kernels bench_mem bench_api bench_options)
add_test(NAME mem_kernels COMMAND mem_kernels_test)

# DLL을 실제로 읽어 짧게 돌려 보는 스모크 테스트 (checksum 불일치면 종료 코드 3으로 실패)
add_test(NAME runner_info COMMAND cpu-bench-runner info)
add_test(NAME runner_int_st COMMAND cpu-bench-runner run int --duration-ms 200 --runs 2)
add_test(NAME runner_int_mt COMMAND cpu-bench-runner run int --mt --duration-ms 200 --runs 2 --json)
add_test(NAME runner_mem_read_mt COMMAND cpu-bench-runner run mem --workload read --size 64M --mt --duration-ms 200 --runs 2)
add_test(NAME runner_mem_copy COMMAND cpu-bench-runner run mem --workload copy --size 64M --duration-ms 200 --runs 2 --json)
add_test(NAME runner_mem_write COMMAND cpu-bench-runner run mem --workload write --size 64M --threads 2 --duration-ms 200 --runs 2)
add_test(NAME runner_mem_sweep COMMAND cpu-bench-runner sweep mem --workload latency --from 16K --to 4M --duration-ms 100 --runs 2)

# 부동소수점 커널: scalar·avx2 경로 모두 Python 독립 구현과 비트 단위로 같아야 한다
add_executable(fp_kernels_test fp_kernels_test.cpp)
target_link_libraries(fp_kernels_test PRIVATE fp_kernels bench_options)
add_test(NAME fp_kernels COMMAND fp_kernels_test)
add_test(NAME runner_fp_matmul COMMAND cpu-bench-runner run fp --duration-ms 200 --runs 2)
add_test(NAME runner_fp_nbody_mt COMMAND cpu-bench-runner run fp --workload nbody --mt --duration-ms 200 --runs 2 --json)
add_test(NAME runner_fp_scalar COMMAND cpu-bench-runner run fp --workload matmul --duration-ms 200 --runs 2)
set_tests_properties(runner_fp_scalar PROPERTIES ENVIRONMENT "CPU_BENCH_FP_PATH=scalar")

# SMBIOS 메모리 정보 해석 (직접 만든 표로 확인)
add_executable(smbios_test smbios_test.cpp ${CMAKE_SOURCE_DIR}/runner/smbios.cpp)
target_include_directories(smbios_test PRIVATE ${CMAKE_SOURCE_DIR}/runner)
target_link_libraries(smbios_test PRIVATE bench_options)
add_test(NAME smbios COMMAND smbios_test)
add_test(NAME runner_info_json COMMAND cpu-bench-runner info --json)
add_test(NAME runner_check COMMAND cpu-bench-runner check --seconds 1 --json)

# 메모리 파트 번호 → 정격 속도, 메모리 진단
add_executable(ramspec_test ramspec_test.cpp ${CMAKE_SOURCE_DIR}/runner/ramspec.cpp ${CMAKE_SOURCE_DIR}/runner/envcheck.cpp ${CMAKE_SOURCE_DIR}/runner/smbios.cpp)
target_include_directories(ramspec_test PRIVATE ${CMAKE_SOURCE_DIR}/runner)
target_link_libraries(ramspec_test PRIVATE bench_options)
add_test(NAME ramspec COMMAND ramspec_test)

# 유효 클럭 측정 (MASM 덧셈 체인)
add_executable(clock_test clock_test.cpp)
target_link_libraries(clock_test PRIVATE cpu_clock bench_options)
add_test(NAME clock COMMAND clock_test)
add_test(NAME runner_clock COMMAND cpu-bench-runner clock --json)
add_test(NAME runner_throttle COMMAND cpu-bench-runner throttle --seconds 5 --json)

# 점수 체계 v1
add_executable(score_test score_test.cpp ${CMAKE_SOURCE_DIR}/runner/score.cpp)
target_include_directories(score_test PRIVATE ${CMAKE_SOURCE_DIR}/runner)
target_link_libraries(score_test PRIVATE bench_options)
add_test(NAME score COMMAND score_test)
add_test(NAME runner_score COMMAND cpu-bench-runner score --runs 1 --duration-ms 200 --json)

# PCIe 링크 판정 (직접 만든 장치 목록으로 확인)
add_executable(pcie_test pcie_test.cpp ${CMAKE_SOURCE_DIR}/runner/pcie.cpp)
target_include_directories(pcie_test PRIVATE ${CMAKE_SOURCE_DIR}/runner)
target_link_libraries(pcie_test PRIVATE bench_options setupapi)
add_test(NAME pcie COMMAND pcie_test)

# 네트워크 연결 판정
add_executable(netlink_test netlink_test.cpp ${CMAKE_SOURCE_DIR}/runner/netlink.cpp)
target_include_directories(netlink_test PRIVATE ${CMAKE_SOURCE_DIR}/runner)
target_link_libraries(netlink_test PRIVATE bench_options iphlpapi)
add_test(NAME netlink COMMAND netlink_test)
