package ipc

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
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/ipv4"
)

type MessageHandler func(data []byte) error
type StateHandler func(state ConnectionState) error

type ConnectionState int32

const (
	NotConnected ConnectionState = iota
	ConnectionPending
	Connected
	Disconnecting
	Disconnected
)

type AtomicConnectionState struct {
	value int32
}

func NewAtomicConnectionState(initial ConnectionState) AtomicConnectionState {
	return AtomicConnectionState{
		value: int32(initial),
	}
}

func (a *AtomicConnectionState) Get() ConnectionState {
	return ConnectionState(atomic.LoadInt32(&a.value))
}

func (a *AtomicConnectionState) Set(val ConnectionState) {
	atomic.StoreInt32(&a.value, int32(val))
}

type ConnectionRole string

const (
	ClientConnection ConnectionRole = "client"
	ServerConnection ConnectionRole = "server"

	DefaultServerHost        = "127.0.0.1"
	DefaultServerPort        = 33333
	DefaultConnectionTimeout = time.Second * 60
	DefaultReadTimeout       = time.Millisecond * 500
	DefaultWriteTimeout      = time.Millisecond * 500
)

type Connection interface {
	ListenAndServe(ctx context.Context, callbackFn StateHandler) error
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
	IsConnected() bool
}

type connection struct {
	state             AtomicConnectionState
	role              ConnectionRole
	serverHost        string
	serverPort        int
	conn              *ipv4.PacketConn
	identifier        string
	recv              chan *Message
	done              chan struct{}
	connectionTimeout time.Duration
	readTimeout       time.Duration
	writeTimeout      time.Duration
	peerAddress       net.Addr
	readHandler       MessageHandler
	readLocker        sync.Mutex
	writeLocker       sync.Mutex
}

type OptionFunction func(*connection)

func WithServerHost(host string) OptionFunction {
	return func(c *connection) {
		c.serverHost = host
	}
}

func WithServerPort(port int) OptionFunction {
	return func(c *connection) {
		c.serverPort = port
	}
}

func WithConnectionTimeout(timeout time.Duration) OptionFunction {
	return func(c *connection) {
		c.connectionTimeout = timeout
	}
}

func WithReadTimeout(timeout time.Duration) OptionFunction {
	return func(c *connection) {
		c.readTimeout = timeout
	}
}

func WithWriteTimeout(timeout time.Duration) OptionFunction {
	return func(c *connection) {
		c.writeTimeout = timeout
	}
}

func WithReadHandler(handler MessageHandler) OptionFunction {
	return func(c *connection) {
		c.readHandler = handler
	}
}

func NewConnection(role ConnectionRole, opts ...OptionFunction) (Connection, error) {
	conn := &connection{
		state:             NewAtomicConnectionState(NotConnected),
		role:              role,
		serverHost:        DefaultServerHost,
		serverPort:        DefaultServerPort,
		identifier:        uuid.NewString(),
		recv:              make(chan *Message),
		done:              make(chan struct{}),
		connectionTimeout: DefaultConnectionTimeout,
		readTimeout:       DefaultReadTimeout,
		writeTimeout:      DefaultWriteTimeout,
		peerAddress:       nil,
		readLocker:        sync.Mutex{},
		writeLocker:       sync.Mutex{},
	}

	for _, opt := range opts {
		opt(conn)
	}

	hostIP := net.ParseIP(conn.serverHost)
	if hostIP == nil {
		return nil, errors.New("failed to parse server host IP")
	}

	if conn.role == ClientConnection {
		conn.peerAddress = &net.UDPAddr{IP: hostIP, Port: conn.serverPort}
	}

	return conn, nil
}

func (c *connection) ListenAndServe(ctx context.Context, callbackFn StateHandler) error {
	host := c.serverHost + ":"
	if c.role == ServerConnection {
		host += fmt.Sprintf("%d", c.serverPort)
	}
	sock, err := net.ListenPacket("udp4", host)
	if err != nil {
		return fmt.Errorf("failed to listen on udp: %v", err)
	}

	c.conn = ipv4.NewPacketConn(sock)
	defer c.conn.Close()

	if c.role == ClientConnection {
		err = c.connectClient()
		if err != nil {
			return fmt.Errorf("failed to connect: %v", err)
		}
	}

	err = c.eventLoop(ctx, callbackFn)
	if err != nil {
		return fmt.Errorf("server closed: %v", err)
	}

	err = c.disconnect()
	if err != nil {
		return fmt.Errorf("failed to disconnect: %v", err)
	}

	return nil
}

func (c *connection) Close() error {
	if c.state.Get() == Connected {
		outerCtx, outerCancelFn := context.WithCancelCause(context.Background())
		defer outerCancelFn(nil)

		ctx, cancelFn := context.WithTimeout(outerCtx, c.writeTimeout)
		defer cancelFn()

		err := c.write(ctx, &Message{ID: uuid.NewString(), Type: Disconnect, Source: c.identifier}, c.peerAddress)
		if err != nil {
			outerCancelFn(fmt.Errorf("write error: %v", err))
		}

		c.done <- struct{}{}
	}
	return nil
}

func (c *connection) Read(ctx context.Context) ([]byte, error) {
	c.readLocker.Lock()
	defer c.readLocker.Unlock()

	if c.readHandler != nil {
		return nil, fmt.Errorf("read handler is set")
	}

	err := c.ensureConnected(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot write: %v", err)
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled")
	case msg := <-c.recv:
		return msg.Data, nil
	case <-time.After(c.readTimeout):
		return nil, fmt.Errorf("read timeout")
	}
}

func (c *connection) Write(ctx context.Context, data []byte) error {
	c.writeLocker.Lock()
	defer c.writeLocker.Unlock()

	err := c.ensureConnected(ctx)
	if err != nil {
		return fmt.Errorf("cannot write: %v", err)
	}

	msg := &Message{
		ID:     uuid.NewString(),
		Type:   Data,
		Data:   data,
		Source: c.identifier,
	}

	return c.write(ctx, msg, c.peerAddress)
}

func (c *connection) ensureConnected(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context cancelled")
	}

	var err error
	if c.state.Get() != Connected {
		done := make(chan struct{})
		go func() {
			defer func() {
				done <- struct{}{}
			}()

			t := time.NewTimer(time.Millisecond * 10)
			defer t.Stop()

			for {
				select {
				case <-t.C:
					if c.state.Get() == Connected {
						return
					}
				case <-ctx.Done():
					err = ctx.Err()
					return
				case <-time.After(c.connectionTimeout):
					err = fmt.Errorf("connection timeout")
					return
				case <-c.done:
					return
				}
			}
		}()
		<-done
	}

	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}

	if c.state.Get() != Connected {
		return fmt.Errorf("not connected")
	}

	return nil
}

func (c *connection) connectClient() error {
	outerCtx, outerCancelFn := context.WithCancelCause(context.Background())
	defer outerCancelFn(nil)

	ctx, ctxCancelFn := context.WithTimeout(outerCtx, c.connectionTimeout)
	defer ctxCancelFn()

	c.state.Set(ConnectionPending)

	done := make(chan struct{})
	go func() {
		// signals that the connection is established
		defer func() {
			done <- struct{}{}
		}()

		for {
			if ctx.Err() != nil {
				return
			}

			err := c.write(ctx, &Message{ID: uuid.NewString(), Type: Connect, Source: c.identifier}, c.peerAddress)
			if err != nil {
				outerCancelFn(fmt.Errorf("write error: %v", err))
			}

			msg, addr, err := c.read(ctx)
			if err != nil {
				outerCancelFn(fmt.Errorf("read error: %v", err))
			}

			if msg == nil {
				continue
			}

			if msg.Type == Ack && addr.String() == c.peerAddress.String() {
				c.state.Set(Connected)
				return
			}

			time.Sleep(time.Millisecond * 500)
		}
	}()

	<-done

	return ctx.Err()
}

func (c *connection) disconnect() error {
	if c.state.Get() != Connected {
		c.state.Set(Disconnecting)
		ctx, cancelFn := context.WithTimeout(context.Background(), c.writeTimeout)
		defer cancelFn()

		err := c.write(ctx, &Message{ID: uuid.NewString(), Type: Disconnect, Source: c.identifier}, c.peerAddress)
		if err != nil {
			return fmt.Errorf("failed to disconnect: %v", err)
		}
	}
	c.peerAddress = nil
	c.state.Set(NotConnected)
	return nil
}

func (c *connection) eventLoop(ctx context.Context, callbackFn StateHandler) error {
	for {
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return nil
		default:
			err := c.waitForNextMessage(ctx, callbackFn)
			if err != nil {
				return err
			}
		}
	}
}

func (c *connection) waitForNextMessage(ctx context.Context, callbackFn StateHandler) error {
	msg, addr, err := c.read(ctx)
	if err != nil {
		return fmt.Errorf("read failed: %v", err)
	}
	if msg == nil {
		return nil
	}

	switch msg.Type {
	case Connect:
		c.peerAddress = addr
		c.state.Set(Connected)
		c.write(ctx, &Message{ID: uuid.NewString(), Type: Ack, Source: c.identifier}, c.peerAddress)
		callbackFn(c.state.Get())
	case Disconnect:
		if addr.String() == c.peerAddress.String() {
			c.peerAddress = nil
			c.state.Set(NotConnected)
			callbackFn(c.state.Get())
		}
	case Data:
		if addr.String() == c.peerAddress.String() {
			if c.readHandler != nil {
				err = c.readHandler(msg.Data)
				if err != nil {
					return fmt.Errorf("failed to handle read: %v", err)
				}
			} else {
				c.recv <- msg
			}
		}
	}

	return nil
}

func (c *connection) read(ctx context.Context) (*Message, net.Addr, error) {
	err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set read deadline: %v", err)
	}

	data := make([]byte, 1024)
	nBytes, _, addr, err := c.conn.ReadFrom(data)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("failed to read from connection: %v", err)
	}
	if nBytes == 0 {
		return nil, nil, fmt.Errorf("no data read from connection")
	}
	if nBytes == 1024 {
		return nil, nil, fmt.Errorf("buffer overflow")
	}

	msg := &Message{}
	err = decodeMessage(data[:nBytes], msg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode message: %v", err)
	}

	return msg, addr, nil
}

func (c *connection) write(ctx context.Context, msg *Message, netAddr net.Addr) error {
	err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	if err != nil {
		return fmt.Errorf("failed to set write deadline: %v", err)
	}

	data, err := encodeMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to encode message: %v", err)
	}
	n, err := c.conn.WriteTo(data, nil, netAddr)
	if err != nil {
		return fmt.Errorf("failed to write to connection: %v", err)
	}
	if n != len(data) {
		return fmt.Errorf("failed to write all data to connection")
	}

	return nil
}

func (c *connection) IsConnected() bool {
	return c.state.Get() == Connected
}
