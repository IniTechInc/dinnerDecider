# DinnerDecider JNI shim (plan Step 35).
#
# Built OUT OF TREE by ../build-native.sh against an already-built llama.cpp, so
# the app's Gradle build never needs the NDK and `assembleDebug` works on a fresh
# clone with an empty jniLibs directory.
#
# Two configure-time variables are required, both supplied by build-native.sh:
#   DD_LLAMA_SRC_DIR  the llama.cpp source tree (headers)
#   DD_LLAMA_BIN_DIR  the directory holding the built .so files (link targets)

cmake_minimum_required(VERSION 3.22)

# --------------------------------------------------------------------------
# ANDROID_STL MUST be c++_shared, and it must be set HERE, not only on the
# command line of whoever invokes this file.
#
# The NDK default is c++_static, which links a PRIVATE copy of the C++ runtime
# into this library. Loading a c++_static shim into the same process as the
# c++_shared libllama.so / libmtmd.so / libllama-common.so means two C++
# runtimes, two sets of type_info, two operator new implementations and two
# exception tables in one address space. That is undefined behavior, and it is
# the exact hazard the flag exists to prevent. Specifying only ANDROID_ABI
# leaves the STL at the default and looks like it worked.
#
# The set() must precede project(): the NDK toolchain file is read during
# project(), and that is when it consumes ANDROID_STL.
# --------------------------------------------------------------------------
set(ANDROID_STL "c++_shared" CACHE STRING "C++ runtime: must match the llama.cpp libraries" FORCE)

project(dinnerdecider_llama CXX)

if(NOT ANDROID)
    message(FATAL_ERROR "This shim is Android-only. Configure with the NDK toolchain file.")
endif()

if(NOT ANDROID_STL STREQUAL "c++_shared")
    message(FATAL_ERROR
        "ANDROID_STL is '${ANDROID_STL}' but must be 'c++_shared'. "
        "Mixing a private C++ runtime with the c++_shared llama.cpp libraries is undefined behavior.")
endif()

if(NOT DEFINED DD_LLAMA_SRC_DIR OR NOT EXISTS "${DD_LLAMA_SRC_DIR}/include/llama.h")
    message(FATAL_ERROR "DD_LLAMA_SRC_DIR must point at a llama.cpp source tree (got '${DD_LLAMA_SRC_DIR}')")
endif()
if(NOT DEFINED DD_LLAMA_BIN_DIR OR NOT EXISTS "${DD_LLAMA_BIN_DIR}/libllama.so")
    message(FATAL_ERROR "DD_LLAMA_BIN_DIR must point at the built llama.cpp libraries (got '${DD_LLAMA_BIN_DIR}')")
endif()
if(NOT EXISTS "${DD_LLAMA_BIN_DIR}/libmtmd.so")
    message(FATAL_ERROR
        "libmtmd.so is missing from ${DD_LLAMA_BIN_DIR}. "
        "LLAMA_BUILD_TOOLS=OFF without LLAMA_BUILD_MTMD=ON deletes it and vision dies silently at runtime.")
endif()

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

# Import the prebuilt llama.cpp libraries. llama-common is the target name (the
# artifact is libllama-common.so); a target literally named `common` does not
# exist and linking -lcommon fails.
foreach(dd_lib llama mtmd llama-common ggml ggml-base ggml-cpu)
    add_library(dd::${dd_lib} SHARED IMPORTED)
    set_target_properties(dd::${dd_lib} PROPERTIES
        IMPORTED_LOCATION "${DD_LLAMA_BIN_DIR}/lib${dd_lib}.so")
endforeach()

add_library(dinnerdecider_llama SHARED dd_llama_jni.cpp)

# llama-common's headers are NOT installed, so the source tree goes on the
# include path directly. Its public surface is plain C++ with no extern "C",
# which is why this shim is C++ and must be ABI-matched to the NDK that built it.
target_include_directories(dinnerdecider_llama PRIVATE
    "${DD_LLAMA_SRC_DIR}/include"
    "${DD_LLAMA_SRC_DIR}/ggml/include"
    "${DD_LLAMA_SRC_DIR}/common"
    "${DD_LLAMA_SRC_DIR}/tools/mtmd"
    "${DD_LLAMA_SRC_DIR}/vendor"
    "${DD_LLAMA_SRC_DIR}/vendor/nlohmann"
)

target_link_libraries(dinnerdecider_llama
    PRIVATE
        dd::mtmd
        dd::llama-common
        dd::llama
        dd::ggml
        dd::ggml-base
        android
        log
)

target_compile_options(dinnerdecider_llama PRIVATE -fvisibility=hidden -Wall -Wextra)
