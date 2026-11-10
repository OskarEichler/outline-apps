# TCP receive buffering while the proxy connects

The pinned go-tun2socks replacement in `go.mod` fixes early TCP data loss in
v1.16.11. The original receive callback refuses data while the proxy dialer
runs. lwIP retains the first segment in `refused_data` and drops later segments
until the dial completes, forcing a retransmission for multi-segment handshakes.
A default large TLS 1.3 ClientHello reproduced this; the smaller TLS 1.2 and
X25519-only controls did not. TLS security settings are unchanged.

The fork buffers incoming data in a ring bounded by the existing 32 KiB TCP
receive window. It accepts data during dialing without blocking the shared lwIP
thread, credits the window when readers drain data, and preserves ordered reads,
FIN/EOF draining, reset and close cancellation. It does not enlarge TCP windows
or change server settings. The root Go dependency covers all users of this
network stack; the installed macOS GUI and extension are both rebuilt.

A synthetic raw IPv4/TCP harness reproduced the original missing second segment.
The fixed build delivered it without retransmission. Race-enabled checks covered
FIN during dialing, a full 32 KiB unread window, window reopening, a 64 KiB ring
wrap, reset, failed dialing and CloseRead. Default dependency `go vet` reports
existing unreachable code and integer-to-string diagnostics; other vet checks
pass. Live measurements and release verification are recorded separately.

This is a pinned first-party fork, not an upstream Outline release. Preserve the
replacement until an upstream dependency incorporates equivalent behavior.
