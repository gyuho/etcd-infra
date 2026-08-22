package scenarios

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[ConcurrentPuts-0]
	_ = x[BurstWrites-1]
	_ = x[SustainedLoad-2]
	_ = x[RampUpLoad-3]
	_ = x[MixedWorkload-4]
	_ = x[LargeValues-5]
	_ = x[ManyKeys-6]
	_ = x[HighContention-7]
	_ = x[SequentialWrites-8]
	_ = x[RandomReads-9]
	_ = x[WatchProgressNotify-10]
	_ = x[LeaseIntensiveWorkload-11]
	_ = x[ListPaginationHeavy-12]
	_ = x[OptimisticConcurrencyTxn-13]
	_ = x[WatchManyPrefixes-14]
	_ = x[CompactDuringLoad-15]
	_ = x[WatchWithChurn-16]
	_ = x[NamespaceIsolationHeavy-17]
	_ = x[TxnMultiKeyUpdate-18]
	_ = x[LeaderElectionContention-19]
	_ = x[WatchBookmarkHeavy-20]
	_ = x[K8sPodLifecycleChurn-21]
	_ = x[K8sNodeHeartbeatLeases-22]
	_ = x[K8sMixedApiserver-23]
	_ = x[K8sCRDHeavyChurn-24]
}

const _StressIDName = "CONCURRENT_PUTSBURST_WRITESSUSTAINED_LOADRAMP_UP_LOADMIXED_WORKLOADLARGE_VALUESMANY_KEYSHIGH_CONTENTIONSEQUENTIAL_WRITESRANDOM_READSWATCH_PROGRESS_NOTIFYLEASE_INTENSIVE_WORKLOADLIST_PAGINATION_HEAVYOPTIMISTIC_CONCURRENCY_TXNWATCH_MANY_PREFIXESCOMPACT_DURING_LOADWATCH_WITH_CHURNNAMESPACE_ISOLATION_HEAVYTXN_MULTI_KEY_UPDATELEADER_ELECTION_CONTENTIONWATCH_BOOKMARK_HEAVYK8S_POD_LIFECYCLE_CHURNK8S_NODE_HEARTBEAT_LEASESK8S_MIXED_APISERVERK8S_CRD_HEAVY_CHURN"

var _StressIDIndex = [...]uint16{0, 15, 27, 41, 53, 67, 79, 88, 103, 120, 132, 153, 177, 198, 224, 243, 262, 278, 303, 323, 349, 369, 392, 417, 436, 455}

func (i StressID) String() string {
	idx := int(i) - 0
	if i < 0 || idx >= len(_StressIDIndex)-1 {
		return "StressID(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _StressIDName[_StressIDIndex[idx]:_StressIDIndex[idx+1]]
}
