cmake_minimum_required(VERSION 3.21)

# 버전은 VERSION 파일 하나에서 읽는다 (러너·앱·배포 zip이 같은 값을 쓴다)
file(STRINGS ${CMAKE_CURRENT_SOURCE_DIR}/VERSION CPU_BENCH_VERSION LIMIT_COUNT 1)
string(STRIP "${CPU_BENCH_VERSION}" CPU_BENCH_VERSION)  # CRLF로 받은 경우 \r 제거
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS ${CMAKE_CURRENT_SOURCE_DIR}/VERSION)
project(cpu_bench VERSION ${CPU_BENCH_VERSION} LANGUAGES C CXX)

if(NOT WIN32)
  message(FATAL_ERROR "cpu-bench is Windows-only")
endif()

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# 실행 파일과 모듈 DLL을 한 폴더에 모은다 (러너가 옆의 bench_<id>.dll을 찾음)
foreach(cfg "" _DEBUG _RELEASE _RELWITHDEBINFO)
  set(CMAKE_RUNTIME_OUTPUT_DIRECTORY${cfg} ${CMAKE_BINARY_DIR}/bin)
  set(CMAKE_LIBRARY_OUTPUT_DIRECTORY${cfg} ${CMAKE_BINARY_DIR}/bin)
endforeach()

# 공통 컴파일 옵션
add_library(bench_options INTERFACE)
target_compile_definitions(bench_options INTERFACE UNICODE _UNICODE _CRT_SECURE_NO_WARNINGS)
if(MSVC)
  # C/C++에만 적용 (MASM 파일에는 넘기지 않는다)
  target_compile_options(bench_options INTERFACE
    $<$<COMPILE_LANGUAGE:C,CXX>:/W4 /permissive- /utf-8 /EHsc>
    $<$<AND:$<COMPILE_LANGUAGE:C,CXX>,$<CONFIG:Release,RelWithDebInfo>>:/O2 /Oi /GS->)
  # 정적 CRT: 배포 시 VC++ 재배포 패키지 설치가 필요 없게 한다
  set(CMAKE_MSVC_RUNTIME_LIBRARY "MultiThreaded$<$<CONFIG:Debug>:Debug>")
endif()

add_library(bench_api INTERFACE)
target_include_directories(bench_api INTERFACE ${CMAKE_CURRENT_SOURCE_DIR}/include)

# 모듈 공통 측정 루프 (header-only)
add_library(bench_harness INTERFACE)
target_include_directories(bench_harness INTERFACE ${CMAKE_CURRENT_SOURCE_DIR}/modules/common)
target_link_libraries(bench_harness INTERFACE bench_api)

add_subdirectory(modules/int)
add_subdirectory(modules/mem)
add_subdirectory(modules/fp)
add_subdirectory(modules/mt)
add_subdirectory(runner)

include(CTest)
if(BUILD_TESTING)
  add_subdirectory(tests)
endif()
