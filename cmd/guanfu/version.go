package main

// version 由 CI 注入：-ldflags "-X main.version=v1.2.3"；本地 go build 时为 "dev"。
//
// 这个变量必须被真正引用（见 main 里的 -version flag 与启动日志），
// 否则链接器会把它当未使用符号优化掉，`-X` 静默失效——不报错、不警告，
// 二进制 --version 永远打印 dev。
var version = "dev"
