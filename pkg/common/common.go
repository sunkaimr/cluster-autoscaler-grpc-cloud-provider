package common

import (
	"context"
	"sync"
)

func ContextWaitGroupAdd(ctx context.Context, add int) {
	v := ctx.Value("wg")
	if v == nil {
		return
	}

	if wg, ok := v.(*sync.WaitGroup); ok {
		wg.Add(add)
	}
}

func ContextWaitGroupDone(ctx context.Context) {
	v := ctx.Value("wg")
	if v == nil {
		return
	}

	if wg, ok := v.(*sync.WaitGroup); ok {
		wg.Done()
	}
}

func ContextWaitGroupWait(ctx context.Context) {
	v := ctx.Value("wg")
	if v == nil {
		return
	}

	if wg, ok := v.(*sync.WaitGroup); ok {
		wg.Wait()
	}
}
