// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Drone Non-Commercial License
// that can be found in the LICENSE file.

package queue

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/drone/drone/core"
)

type queue struct {
	sync.Mutex

	ready    chan struct{}
	paused   bool
	interval time.Duration
	store    core.StageStore
	workers  map[*worker]struct{}
	ctx      context.Context
}

// newQueue returns a new Queue backed by the build datastore.
func newQueue(store core.StageStore) *queue {
	q := &queue{
		store:    store,
		ready:    make(chan struct{}, 1),
		workers:  map[*worker]struct{}{},
		interval: time.Minute,
		ctx:      context.Background(),
	}
	go q.start()
	return q
}

func (q *queue) Schedule(ctx context.Context, stage *core.Stage) error {
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return nil
}

func (q *queue) Pause(ctx context.Context) error {
	q.Lock()
	q.paused = true
	q.Unlock()
	return nil
}

func (q *queue) Paused(ctx context.Context) (bool, error) {
	q.Lock()
	paused := q.paused
	q.Unlock()
	return paused, nil
}

func (q *queue) Resume(ctx context.Context) error {
	q.Lock()
	q.paused = false
	q.Unlock()

	select {
	case q.ready <- struct{}{}:
	default:
	}
	return nil
}

func (q *queue) Request(ctx context.Context, params core.Filter) (*core.Stage, error) {
	w := &worker{
		os:      params.OS,
		arch:    params.Arch,
		kernel:  params.Kernel,
		variant: params.Variant,
		labels:  params.Labels,
		channel: make(chan *core.Stage),
	}
	q.Lock()
	q.workers[w] = struct{}{}
	q.Unlock()

	select {
	case q.ready <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		q.Lock()
		delete(q.workers, w)
		q.Unlock()
		return nil, ctx.Err()
	case b := <-w.channel:
		return b, nil
	}
}

func (q *queue) signal(ctx context.Context) error {
	q.Lock()
	count := len(q.workers)
	pause := q.paused
	q.Unlock()
	if pause {
		return nil
	}
	if count == 0 {
		return nil
	}
	items, err := q.store.ListIncomplete(ctx)
	if err != nil {
		return err
	}

	q.Lock()
	defer q.Unlock()
	for _, item := range items {
		// if the stage has build related concurrency limits we need
		// to deal with those
		if !withinBranchLimits(item, items) {
			continue
		}
		if item.Status == core.StatusRunning {
			continue
		}
		if item.Machine != "" {
			continue
		}

		// if the stage defines concurrency limits we
		// need to make sure those limits are not exceeded
		// before proceeding.
		if !withinLimits(item, items) {
			continue
		}

	loop:
		for w := range q.workers {
			// the worker is platform-specific. check to ensure
			// the queue item matches the worker platform.
			if w.os != item.OS {
				continue
			}
			if w.arch != item.Arch {
				continue
			}
			// if the pipeline defines a variant it must match
			// the worker variant (e.g. arm6, arm7, etc).
			if item.Variant != "" && item.Variant != w.variant {
				continue
			}
			// if the pipeline defines a kernel version it must match
			// the worker kernel version (e.g. 1709, 1803).
			if item.Kernel != "" && item.Kernel != w.kernel {
				continue
			}
			if len(item.Labels) > 0 || len(w.labels) > 0 {
				if !checkLabels(item.Labels, w.labels) {
					continue
				}
			}

			// // the queue has 60 seconds to ack the item, otherwise
			// // it is eligible for processing by another worker.
			// // item.Expires = time.Now().Add(time.Minute).Unix()
			// err := q.store.Update(ctx, item)

			// if err != nil {
			// 	log.Ctx(ctx).Warn().
			// 		Err(err).
			// 		Int64("build_id", item.BuildID).
			// 		Int64("stage_id", item.ID).
			// 		Msg("cannot update queue item")
			// 	continue
			// }
			select {
			case w.channel <- item:
				delete(q.workers, w)
				break loop
			}
		}
	}
	return nil
}

func (q *queue) start() error {
	for {
		select {
		case <-q.ctx.Done():
			return q.ctx.Err()
		case <-q.ready:
			if err := q.signal(q.ctx); err != nil {
				fmt.Printf("SIGNAL FUCKED: %v\n", err)
			}
		case <-time.After(q.interval):
			if err := q.signal(q.ctx); err != nil {
				fmt.Printf("SIGNAL FUCKED: %v\n", err)
			}
		}
	}
}

type worker struct {
	os      string
	arch    string
	kernel  string
	variant string
	labels  map[string]string
	channel chan *core.Stage
}

type counter struct {
	counts map[string]int
}

func checkLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

func withinLimits(stage *core.Stage, siblings []*core.Stage) bool {
	if stage.Limit == 0 {
		return true
	}
	count := 0
	for _, sibling := range siblings {
		if sibling.RepoID != stage.RepoID {
			continue
		}
		if sibling.ID == stage.ID {
			continue
		}
		if sibling.Name != stage.Name {
			continue
		}
		if sibling.ID < stage.ID {
			count++
		}
	}
	return count < stage.Limit
}

func withinBranchLimits(stage *core.Stage, siblings []*core.Stage) bool {
	// if the build for this stage is running, then we should be allowed to run
	if stage.Build.Status == core.StatusRunning {
		return true
	}

	// if there are any other stages for other builds, check that
	for _, sibling := range siblings {
		if sibling.BuildID == stage.BuildID || sibling.RepoID != stage.RepoID {
			continue
		}

		// If there is another build of master, don't do it
		if sibling.Build.Source == "master" && stage.Build.Source == "master" {
			// If we are older than our sibling, we should go ahead and build ourselves
			if stage.BuildID < sibling.BuildID {
				return true
			}
			return false
		}
	}
	return true
}
