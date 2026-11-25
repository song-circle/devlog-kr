# 큐·값 생성 커널 (단위 테스트에서도 링크)
add_library(mt_kernels STATIC mt_kernels.cpp)
target_include_directories(mt_kernels PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(mt_kernels PRIVATE bench_options)

# 부하 모듈 DLL: bench_mt.dll
add_library(bench_mt SHARED mt_module.cpp)
set_target_properties(bench_mt PROPERTIES PREFIX "")
target_link_libraries(bench_mt PRIVATE bench_harness bench_options mt_kernels)
