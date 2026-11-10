// Copyright 2026 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package domainbypass

import (
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.getoutline.org/sdk/network/packetrelay"
)

func TestReceiveQueuePressureKeepsProxyAssociationOpen(t *testing.T) {
	proxy := &controlledPacketRelay{
		responses: make(chan []byte),
		accepted:  make(chan error, 1),
		closed:    make(chan struct{}),
	}
	router := New(Config{}, nil, proxy, nil, nil)
	sender, receiver, err := router.NewAssociation()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	destination := netip.MustParseAddrPort("192.0.2.1:443")
	if err := sender.SendPacket([]byte("request"), destination); err != nil {
		t.Fatal(err)
	}

	// Hold the consumer until the bounded queue fills. A UDP burst may lose
	// datagrams, but it must not close the proxy association.
	for i := 0; i < 17; i++ {
		select {
		case proxy.responses <- []byte{byte(i)}:
		case <-time.After(time.Second):
			t.Fatal("proxy receive loop stopped during burst")
		}
		select {
		case err := <-proxy.accepted:
			if err != nil {
				t.Fatalf("response %d terminated the association: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatal("response handling blocked on the full queue")
		}
	}

	received := make(chan byte, 18)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = receiver.ReceivePackets(packetHandlerFunc(func(p []byte, _ netip.AddrPort) error {
			received <- p[0]
			return nil
		}))
	}()
	for i := 0; i < 16; i++ {
		select {
		case value := <-received:
			if value != byte(i) {
				t.Fatalf("queued response %d: got %d", i, value)
			}
		case <-time.After(time.Second):
			t.Fatal("queued responses were not delivered")
		}
	}
	if err := sender.SendPacket([]byte("retry"), destination); err != nil {
		t.Fatalf("association did not survive queue pressure: %v", err)
	}
	select {
	case proxy.responses <- []byte{99}:
	case <-time.After(time.Second):
		t.Fatal("proxy receive loop stopped after queue pressure")
	}
	select {
	case err := <-proxy.accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("response handling stopped after queue pressure")
	}
	select {
	case value := <-received:
		if value != 99 {
			t.Fatalf("response after pressure: got %d", value)
		}
	case <-time.After(time.Second):
		t.Fatal("response after pressure was not delivered")
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Close did not stop the receiver")
	}
	if err := sender.SendPacket(nil, destination); !errors.Is(err, packetrelay.ErrClosed) {
		t.Fatalf("send after Close: got %v", err)
	}
}

type packetHandlerFunc func([]byte, netip.AddrPort) error

func (f packetHandlerFunc) HandlePacket(p []byte, source netip.AddrPort) error {
	return f(p, source)
}

type controlledPacketRelay struct {
	responses chan []byte
	accepted  chan error
	closed    chan struct{}
	once      sync.Once
}

func (r *controlledPacketRelay) NewAssociation() (packetrelay.PacketSender, packetrelay.PacketReceiver, error) {
	return r, r, nil
}

func (r *controlledPacketRelay) SendPacket([]byte, netip.AddrPort) error { return nil }

func (r *controlledPacketRelay) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func (r *controlledPacketRelay) ReceivePackets(handler packetrelay.PacketHandler) error {
	for {
		select {
		case <-r.closed:
			return nil
		case p := <-r.responses:
			err := handler.HandlePacket(p, netip.MustParseAddrPort("192.0.2.1:443"))
			r.accepted <- err
			if err != nil {
				return err
			}
		}
	}
}
