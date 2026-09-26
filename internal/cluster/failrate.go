package cluster

// Rolling per-node failure rate.
//
// Until this existed, a node's health came only from /api/cluster/ping — a
// trivial endpoint that answers in ~30ms even when the node cannot actually
// serve. Production ran for months with both nodes marked healthy while one
// failed 43% and the other 18% of the requests forwarded to them, because
// MarkFailedRequest merely incremented a lifetime counter that nothing read.
//
// Lifetime counters are also the wrong shape for routing: a node that failed
// 3500 times last week and works fine now would stay condemned forever. So the
// rate is an EWMA over recent outcomes, the same smoothing the latency figure
// already uses, and it decays back once a node recovers.

// failEWMAAlpha weights the newest outcome. 0.1 needs roughly 20-30 requests to
// move the rate across half its range — slow enough that a couple of unlucky
// timeouts do not banish a good node, fast enough to react within seconds at
// production request rates.
const failEWMAAlpha = 0.1

// failScale keeps the rate in an atomic int64 as per-mille (0..1000).
const failScale = 1000

// minFailSamples is how many outcomes must land before the rate is allowed to
// influence routing. A single early failure would otherwise read as "100% bad".
const minFailSamples = 5

// observeOutcome folds one request result into the node's failure rate.
func (n *Node) observeOutcome(failed bool) {
	sample := int64(0)
	if failed {
		sample = failScale
	}
	if n.failSamples.Add(1) == 1 {
		n.failEWMA.Store(sample)
		return
	}
	for {
		cur := n.failEWMA.Load()
		next := int64((1-failEWMAAlpha)*float64(cur) + failEWMAAlpha*float64(sample))
		if n.failEWMA.CompareAndSwap(cur, next) {
			return
		}
	}
}

// FailRate returns the recent failure rate in 0..1. It reports 0 until
// minFailSamples outcomes have been seen, so a cold node is not penalised for
// having no history.
func (n *Node) FailRate() float64 {
	if n.failSamples.Load() < minFailSamples {
		return 0
	}
	v := n.failEWMA.Load()
	if v <= 0 {
		return 0
	}
	if v > failScale {
		v = failScale
	}
	return float64(v) / failScale
}

// failPenalty converts the failure rate into a multiplier on a node's routing
// score, so the score reads as "cost per SUCCESSFUL request" rather than cost
// per attempt: a node that fails half of what it is given costs twice as much
// to use, because half the work has to be redone somewhere else.
//
// The divisor is floored so a fully broken node ends up expensive (×10) rather
// than infinitely so — it must still be reachable for recovery probes, and the
// pool needs a finite score to sort by.
func (n *Node) failPenalty() float64 {
	success := 1 - n.FailRate()
	if success < 0.1 {
		success = 0.1
	}
	return 1 / success
}
