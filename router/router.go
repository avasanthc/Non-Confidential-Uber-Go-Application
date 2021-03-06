// Copyright (c) 2015 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package router

import (
	"sync"

	"github.com/uber/ringpop-go"
	"github.com/uber/ringpop-go/events"
	"github.com/uber/ringpop-go/swim"
	"github.com/uber/tchannel-go"
	"github.com/uber/tchannel-go/thrift"
)

type router struct {
	ringpop ringpop.Interface
	factory ClientFactory
	channel *tchannel.Channel

	rw          sync.RWMutex
	clientCache map[string]interface{}
}

// A Router creates instances of TChannel Thrift Clients via the help of the ClientFactory
type Router interface {
	GetClient(key string) (interface{}, error)
}

// A ClientFactory is able to provide an implementation of a TChan[Service]
// interface that can dispatch calls to the actual implementation. This could be
// both a local or a remote implementation of the interface based on the dest
// provided
type ClientFactory interface {
	GetLocalClient() interface{}
	MakeRemoteClient(client thrift.TChanClient) interface{}
}

// New creates an instance that validates the Router interface. A Router
// will be used to get implementations of service interfaces that implement a
// distributed microservice.
func New(rp ringpop.Interface, f ClientFactory, ch *tchannel.Channel) Router {
	r := &router{
		ringpop:     rp,
		factory:     f,
		clientCache: make(map[string]interface{}),
		channel:     ch,
	}
	rp.RegisterListener(r)
	return r
}

func (r *router) HandleEvent(event events.Event) {
	switch event := event.(type) {
	case swim.MemberlistChangesReceivedEvent:
		for _, change := range event.Changes {
			r.handleChange(change)
		}
	}
}

func (r *router) handleChange(change swim.Change) {
	switch change.Status {
	case swim.Faulty, swim.Leave:
		r.removeClient(change.Address)
	}
}

// Get the client for a certain destination from our internal cache, or
// delegates the creation to the ClientFactory.
func (r *router) GetClient(key string) (interface{}, error) {
	dest, err := r.ringpop.Lookup(key)
	if err != nil {
		return nil, err
	}

	r.rw.RLock()
	client, ok := r.clientCache[dest]
	r.rw.RUnlock()
	if ok {
		return client, nil
	}

	// no match so far, get a complete lock for creation
	r.rw.Lock()
	defer r.rw.Unlock()

	// double check it is not created between read and complete lock
	client, ok = r.clientCache[dest]
	if ok {
		return client, nil
	}

	me, err := r.ringpop.WhoAmI()
	if err != nil {
		return nil, err
	}

	// use the ClientFactory to get the client
	if dest == me {
		client = r.factory.GetLocalClient()
	} else {
		thriftClient := thrift.NewClient(
			r.channel,
			r.channel.ServiceName(),
			&thrift.ClientOptions{
				HostPort: dest,
			},
		)
		client = r.factory.MakeRemoteClient(thriftClient)
	}

	// cache the client
	r.clientCache[dest] = client
	return client, nil
}

func (r *router) removeClient(hostport string) {
	r.rw.Lock()
	delete(r.clientCache, hostport)
	r.rw.Unlock()
}
