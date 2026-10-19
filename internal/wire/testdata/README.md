# Packet stream fixtures

The current byte fixtures live in `packet_test.go` and `packet_fuzz_test.go`. They are constructed manually from the MySQL protocol: a three-byte little-endian payload length, a one-byte sequence number and a maximum packet payload of `0xffffff` bytes. A logical message that exactly fills a packet must append a zero-length terminating packet. An empty message is `00 00 00 00`; a three-byte message is `03 00 00 00 aa bb cc`.

These are minimal, manually constructed protocol vectors, not production packet captures, and contain no credentials. Reads and writes are checked independently against fixed bytes so that round trips through a shared encoder cannot hide errors.
