cmake_minimum_required(VERSION 3.22.1)
project(migi_g2_audio C)
set(LC3 liblc3/src)
add_library(faceclaw_lc3 SHARED faceclaw_lc3_decoder.c
    ${LC3}/attdet.c ${LC3}/bits.c ${LC3}/bwdet.c ${LC3}/energy.c
    ${LC3}/lc3.c ${LC3}/ltpf.c ${LC3}/mdct.c ${LC3}/plc.c
    ${LC3}/sns.c ${LC3}/spec.c ${LC3}/tables.c ${LC3}/tns.c)
target_include_directories(faceclaw_lc3 PRIVATE liblc3/include)
target_compile_definitions(faceclaw_lc3 PRIVATE LC3_PLUS=0 LC3_PLUS_HR=0)
target_link_libraries(faceclaw_lc3 m)
target_link_options(faceclaw_lc3 PRIVATE "-Wl,-z,max-page-size=16384")
