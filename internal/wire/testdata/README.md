# 包流夹具

当前字节夹具写在 packet_test.go / packet_fuzz_test.go 中，人工按 MySQL 协议固定：三字节 little-endian payload 长度、一字节 sequence，单包最大 0xffffff；恰好满包的逻辑消息必须追加零长度终止包。空消息为 `00 00 00 00`，三字节内容为 `03 00 00 00 aa bb cc`。

这些是人工构造的最小协议向量，不是生产抓包，不含凭据；写入与读取分别对照固定字节，避免仅用同一个编码器往返掩盖错误。
