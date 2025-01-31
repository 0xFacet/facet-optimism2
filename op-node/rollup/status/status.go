package status

import (
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/log"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/event"
	"github.com/ethereum-optimism/optimism/op-node/rollup/finality"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

type SetL2BlocksEvent struct {
	UnsafeL2    eth.L2BlockRef
	SafeL2      eth.L2BlockRef
	FinalizedL2 eth.L2BlockRef
	CurrentL1   eth.L1BlockRef
}

func (ev SetL2BlocksEvent) String() string {
	return "set-l2-blocks"
}

type L1UnsafeEvent struct {
	L1Unsafe eth.L1BlockRef
}

func (ev L1UnsafeEvent) String() string {
	return "l1-unsafe"
}

type L1SafeEvent struct {
	L1Safe eth.L1BlockRef
}

func (ev L1SafeEvent) String() string {
	return "l1-safe"
}

type Metrics interface {
	RecordL1ReorgDepth(d uint64)
	RecordL1Ref(name string, ref eth.L1BlockRef)
}

type StatusTracker struct {
	data eth.SyncStatus

	published atomic.Pointer[eth.SyncStatus]

	log log.Logger

	metrics Metrics

	mu sync.RWMutex
}

func NewStatusTracker(log log.Logger, metrics Metrics) *StatusTracker {
	st := &StatusTracker{
		log:     log,
		metrics: metrics,
	}
	st.data = eth.SyncStatus{}
	st.published.Store(&eth.SyncStatus{})
	return st
}

func (st *StatusTracker) OnEvent(ev event.Event) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	switch x := ev.(type) {
	case L1UnsafeEvent:
		st.metrics.RecordL1Ref("l1_head", x.L1Unsafe)
		// We don't need to do anything if the head hasn't changed.
		if st.data.HeadL1 == (eth.L1BlockRef{}) {
			st.log.Info("Received first L1 head signal", "l1_head", x.L1Unsafe)
		} else if st.data.HeadL1.Hash == x.L1Unsafe.Hash {
			st.log.Trace("Received L1 head signal that is the same as the current head", "l1_head", x.L1Unsafe)
		} else if st.data.HeadL1.Hash == x.L1Unsafe.ParentHash {
			// We got a new L1 block whose parent hash is the same as the current L1 head. Means we're
			// dealing with a linear extension (new block is the immediate child of the old one).
			st.log.Debug("L1 head moved forward", "l1_head", x.L1Unsafe)
		} else {
			if st.data.HeadL1.Number >= x.L1Unsafe.Number {
				st.metrics.RecordL1ReorgDepth(st.data.HeadL1.Number - x.L1Unsafe.Number)
			}
			// New L1 block is not the same as the current head or a single step linear extension.
			// This could either be a long L1 extension, or a reorg, or we simply missed a head update.
			st.log.Warn("L1 head signal indicates a possible L1 re-org",
				"old_l1_head", st.data.HeadL1, "new_l1_head_parent", x.L1Unsafe.ParentHash, "new_l1_head", x.L1Unsafe)
		}
		st.data.HeadL1 = x.L1Unsafe
	case L1SafeEvent:
		st.log.Info("New L1 safe block", "l1_safe", x.L1Safe)
		st.metrics.RecordL1Ref("l1_safe", x.L1Safe)
		st.data.SafeL1 = x.L1Safe
	case finality.FinalizeL1Event:
		st.log.Info("New L1 finalized block", "l1_finalized", x.FinalizedL1)
		st.metrics.RecordL1Ref("l1_finalized", x.FinalizedL1)
		st.data.FinalizedL1 = x.FinalizedL1
		st.data.CurrentL1Finalized = x.FinalizedL1
	case rollup.ResetEvent:
		st.data.UnsafeL2 = eth.L2BlockRef{}
		st.data.SafeL2 = eth.L2BlockRef{}
		st.data.CurrentL1 = eth.L1BlockRef{}
	case SetL2BlocksEvent:
		st.data.UnsafeL2 = x.UnsafeL2
		st.data.SafeL2 = x.SafeL2
		st.data.PendingSafeL2 = x.SafeL2
		st.data.FinalizedL2 = x.FinalizedL2
		st.data.CurrentL1 = x.CurrentL1
		st.log.Info("SetL2BlocksEvent", "unsafe", x.UnsafeL2, "safe", x.SafeL2, "finalized", x.FinalizedL2, "CurrentL1", x.CurrentL1)
	default: // other events do not affect the sync status
		return false
	}

	// If anything changes, then copy the state to the published SyncStatus
	// @dev: If this becomes a performance bottleneck during sync (because mem copies onto heap, and 1KB comparisons),
	// we can rate-limit updates of the published data.
	published := *st.published.Load()
	if st.data != published {
		published = st.data
		st.published.Store(&published)
	}
	return true
}

// SyncStatus is thread safe, and reads the latest view of L1 and L2 block labels
func (st *StatusTracker) SyncStatus() *eth.SyncStatus {
	return st.published.Load()
}

// L1Head is a helper function; the L1 head is closely monitored for confirmation-distance logic.
func (st *StatusTracker) L1Head() eth.L1BlockRef {
	return st.SyncStatus().HeadL1
}
