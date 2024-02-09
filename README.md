# gomon-ipc

## Overview
This is a simple cross-platform IPC (interprocess communication protocol) library. It was written to support [gomon](https://github.com/jdudmesh/gomon) but is general enough be used on any project that requires simple interprocess comms. Other libraries do exist but are either too complex or not properly idiomatic Go.

A UDP based network protocol was chosen. This is simple and cross-platform. UDP does not have a reliability layer however, on a single local machine this should not present any problems.

## How it works
The library offers up a `Connection` object which can be configured as a client or server. Both open a UDP socket on the local loopback interface on a well-known port (this can be configured if required). The connection has blocking `Read` and `Write` operations and also a non-blocking read handler. Only one of blocking or non-blocking can be used. Messages are transferred using UDP packets.

All operations make use of `Context` objects and respect context timeout/cancellation.

## Example

See /example/main.go for a working example.