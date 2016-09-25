/*
Copyright IBM Corp. 2016 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

                 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package orderer

import (
	"io"
	"sync"
	"time"

	"github.com/kchristidis/kafka-orderer/ab"
)

// Broadcaster allows the caller to submit messages to the orderer
type Broadcaster interface {
	Broadcast(stream ab.AtomicBroadcast_BroadcastServer) error
	Closeable
}

type broadcasterImpl struct {
	producer Producer
	config   *ConfigImpl
	once     sync.Once

	batchChan  chan *ab.BroadcastMessage
	messages   []*ab.BroadcastMessage
	nextNumber uint64
	prevHash   []byte
}

func newBroadcaster(config *ConfigImpl) Broadcaster {
	return &broadcasterImpl{
		producer:   newProducer(config),
		config:     config,
		batchChan:  make(chan *ab.BroadcastMessage, config.Batch.Size),
		messages:   []*ab.BroadcastMessage{&ab.BroadcastMessage{Data: []byte("genesis")}},
		nextNumber: 0,
	}
}

// Broadcast receives ordering requests by clients and sends back an
// acknowledgement for each received message in order, indicating
// success or type of failure
func (b *broadcasterImpl) Broadcast(stream ab.AtomicBroadcast_BroadcastServer) error {
	b.once.Do(func() {
		// Send the genesis block to create the topic
		// otherwise consumers will throw an exception.
		b.sendBlock()
		// Launch the goroutine that cuts blocks when appropriate.
		go b.cutBlock(b.config.Batch.Period, b.config.Batch.Size)
	})
	return b.recvRequests(stream)
}

// Close shuts down the broadcast side of the orderer
func (b *broadcasterImpl) Close() error {
	if b.producer != nil {
		return b.producer.Close()
	}
	return nil
}

func (b *broadcasterImpl) sendBlock() error {
	block := &ab.Block{
		Messages: b.messages,
		Number:   b.nextNumber,
		PrevHash: b.prevHash,
	}
	Logger.Debugf("Prepared block %d with %d messages (%+v)", block.Number, len(block.Messages), block)

	b.messages = []*ab.BroadcastMessage{}
	b.nextNumber++
	hash, data := hashBlock(block)
	b.prevHash = hash

	return b.producer.Send(data)
}

func (b *broadcasterImpl) cutBlock(period time.Duration, maxSize int) {
	every := time.NewTicker(period)

	for {
		select {
		case msg := <-b.batchChan:
			b.messages = append(b.messages, msg)
			if len(b.messages) == maxSize {
				b.sendBlock()
			}
		case <-every.C:
			if len(b.messages) > 0 {
				b.sendBlock()
			}
		}
	}
}

func (b *broadcasterImpl) recvRequests(stream ab.AtomicBroadcast_BroadcastServer) error {
	reply := new(ab.BroadcastReply)
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		b.batchChan <- msg
		reply.Status = ab.Status_SUCCESS // TODO This shouldn't always be a success
		err = stream.Send(reply)
		if err != nil {
			return err
		}
		Logger.Debugf("Sent broadcast reply %v to client\n", reply.Status.String())
	}
}
