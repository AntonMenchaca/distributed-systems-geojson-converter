package loop

import "fmt"

// Policy is SCHED_POLICY: which queue each receive call reads. Order returns the queue URLs
// to try for one receive, in order; the loop takes messages from the first one that has any.
type Policy interface {
	Order() []string
}

// NewPolicy builds a policy over the queues a worker reads: one queue (QUEUE_LAYOUT=single, or
// one pool of POOL_LAYOUT=split) or [small, large] (QUEUE_LAYOUT=by-size with a shared pool).
//
//	fcfs          one queue, first come first served (the baseline)
//	small-first   read large only when small is empty: small jobs never wait behind large ones
//	weighted-3:1  three reads of small for every read of large, so large jobs still progress
func NewPolicy(name string, queues []string) (Policy, error) {
	switch name {
	case "fcfs":
		if len(queues) != 1 {
			return nil, fmt.Errorf("SCHED_POLICY=fcfs reads one queue, got %d: use QUEUE_LAYOUT=single, or POOL_LAYOUT=split so each pool reads its own queue", len(queues))
		}
		return fcfs{queues[0]}, nil
	case "small-first", "weighted-3:1":
		if len(queues) != 2 {
			return nil, fmt.Errorf("SCHED_POLICY=%s chooses between jobs-small and jobs-large, got %d queue(s): use QUEUE_LAYOUT=by-size with a shared pool", name, len(queues))
		}
		if name == "small-first" {
			return smallFirst{small: queues[0], large: queues[1]}, nil
		}
		return &weighted{small: queues[0], large: queues[1]}, nil
	default:
		return nil, fmt.Errorf("SCHED_POLICY=%q: want fcfs, small-first or weighted-3:1", name)
	}
}

type fcfs struct{ queue string }

func (p fcfs) Order() []string { return []string{p.queue} }

// smallFirst tries large only after small came back empty on the same receive.
type smallFirst struct{ small, large string }

func (p smallFirst) Order() []string { return []string{p.small, p.large} }

// weighted cycles small, small, small, large. When the chosen queue is empty the other one is
// read instead, so a worker never idles while either queue has work. Order is called only from
// the loop's single polling goroutine, so the counter needs no lock.
type weighted struct {
	small, large string
	n            int
}

func (p *weighted) Order() []string {
	p.n++
	if p.n%4 == 0 {
		return []string{p.large, p.small}
	}
	return []string{p.small, p.large}
}
