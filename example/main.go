package main

// gomon is a simple command line tool that watches your files and automatically restarts the application when it detects any changes in the working directory.
// Copyright (C) 2023 John Dudmesh

// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

import (
	"context"
	"fmt"
	"time"

	ipc "github.com/jdudmesh/gomon-ipc"
)

func main() {
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	server, err := ipc.NewConnection(ipc.ServerConnection)
	if err != nil {
		fmt.Println("failed to create server:", err)
		return
	}

	go func() {
		err := server.ListenAndServe(ctx, func(state ipc.ConnectionState) error {
			fmt.Println("server state changed:", state)
			return nil
		})
		if err != nil {
			fmt.Println("server error:", err)
		}
		fmt.Println("server closed")
	}()

	client, err := ipc.NewConnection(ipc.ClientConnection, ipc.WithReadHandler(func(data []byte) error {
		fmt.Println("client received message:", string(data))
		return nil
	}))
	if err != nil {
		fmt.Println("failed to create client:", err)
		return
	}

	go func() {
		err := client.ListenAndServe(ctx, func(state ipc.ConnectionState) error {
			fmt.Println("server state changed:", state)
			return nil
		})
		if err != nil {
			fmt.Println("client error:", err)
		}
		fmt.Println("client closed")
	}()

	writeCtx, writeCancelFn := context.WithTimeout(ctx, time.Second)
	defer writeCancelFn()
	err = client.Write(writeCtx, []byte("Hello, world!"))
	if err != nil {
		fmt.Println("failed to write to server:", err)
	}

	readCtx, readCancelFn := context.WithTimeout(ctx, time.Second)
	defer readCancelFn()
	data, err := server.Read(readCtx)
	if err != nil {
		fmt.Println("failed to read from client:", err)
	}
	fmt.Println("server received message:", string(data))

	err = server.Write(context.Background(), []byte("Hello, client!"))
	if err != nil {
		fmt.Println("failed to write to client:", err)
	}

	time.Sleep(3 * time.Second)
	client.Close()

	time.Sleep(3 * time.Second)
	server.Close()

	fmt.Println("done")
}
