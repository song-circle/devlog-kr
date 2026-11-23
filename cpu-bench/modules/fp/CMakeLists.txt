# 계산 커널 (단위 테스트에서도 링크). AVX2 경로는 그 파일만 /arch:AVX2로 컴파일한다.
add_library(fp_kernels STATIC fp_kernels.cpp fp_avx2.cpp)
target_include_directories(fp_kernels PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(fp_kernels PRIVATE bench_options)
set_source_files_properties(fp_avx2.cpp PROPERTIES COMPILE_OPTIONS "/arch:AVX2")

# 부하 모듈 DLL: bench_fp.dll
add_library(bench_fp SHARED fp_module.cpp)
set_target_properties(bench_fp PROPERTIES PREFIX "")
target_link_libraries(bench_fp PRIVATE bench_harness bench_options fp_kernels)
