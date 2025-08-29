/*
Package queue provides a fast, ring-buffer queue based on the version suggested by Dariusz Górecki.
Using this instead of other, simpler, queue implementations (slice+append or linked list) provides
substantial memory and time benefits, and fewer GC pauses.

The queue implemented here is as fast as it is for an additional reason: it is *not* thread-safe.
*/
package queue

import (
	"reflect"
	"sync"
)

// minQueueLen is smallest capacity that queue may have.
// Must be power of 2 for bitwise modulus: x % n == x & (n - 1).
const minQueueLen = 16

// Queue represents a single instance of the queue data structure.
type Queue struct {
	buf               []interface{}
	head, tail, count int
	lock              sync.RWMutex
}

// New constructs and returns a new Queue.
func New() *Queue {
	return &Queue{
		buf:  make([]interface{}, minQueueLen),
		lock: sync.RWMutex{},
	}
}

// Length returns the number of elements currently stored in the queue.
func (q *Queue) Length() int {
	q.lock.RLock()
	c := q.count
	q.lock.RUnlock()
	return c
}

// resizes the queue to fit exactly twice its current contents
// this can result in shrinking if the queue is less than half-full
func (q *Queue) resize() {
	newBuf := make([]interface{}, q.count<<1)

	if q.tail > q.head {
		copy(newBuf, q.buf[q.head:q.tail])
	} else {
		n := copy(newBuf, q.buf[q.head:])
		copy(newBuf[n:], q.buf[:q.tail])
	}

	q.head = 0
	q.tail = q.count
	q.buf = newBuf
}

// Add puts an element on the end of the queue.
func (q *Queue) Add(elem interface{}, flag bool) bool {
	q.lock.Lock()

	if flag {
		for i := 0; i < q.count; i++ {
			v := q.buf[(q.head+i)&(len(q.buf)-1)]
			if reflect.DeepEqual(elem, v) {
				q.lock.Unlock()
				return false
			}
		}
	}

	if q.count == len(q.buf) {
		q.resize()
	}

	q.buf[q.tail] = elem
	// bitwise modulus
	q.tail = (q.tail + 1) & (len(q.buf) - 1)
	q.count++
	q.lock.Unlock()
	return true
}

// Peek returns the element at the head of the queue. This call panics
// if the queue is empty.
func (q *Queue) Peek() (elem interface{}) {
	q.lock.RLock()
	defer q.lock.RUnlock()

	if q.count <= 0 {
		panic("queue: Peek() called on empty queue")
	}
	elem = q.buf[q.head]
	return elem
}

// Get returns the element at index i in the queue. If the index is
// invalid, the call will panic. This method accepts both positive and
// negative index values. Index 0 refers to the first element, and
// index -1 refers to the last.
func (q *Queue) Get(i int) (elem interface{}) {
	// If indexing backwards, convert to positive index.
	q.lock.RLock()
	defer q.lock.RUnlock()
	if i < 0 {
		i += q.count
	}
	if i < 0 || i >= q.count {
		panic("queue: Get() called with index out of range")
	}
	// bitwise modulus
	elem = q.buf[(q.head+i)&(len(q.buf)-1)]

	return elem
}

// Remove removes and returns the element from the front of the queue. If the
// queue is empty, the call will panic.
func (q *Queue) Remove() interface{} {
	q.lock.Lock()
	defer q.lock.Unlock()
	if q.count <= 0 {
		panic("queue: Remove() called on empty queue")
	}
	ret := q.buf[q.head]
	q.buf[q.head] = nil
	// bitwise modulus
	q.head = (q.head + 1) & (len(q.buf) - 1)
	q.count--
	// Resize down if buffer 1/4 full.
	if len(q.buf) > minQueueLen && (q.count<<2) == len(q.buf) {
		q.resize()
	}
	return ret
}
