//go:build etcd_infra_custom_client

package client

import clientv3 "go.etcd.io/etcd/client/v3"

// Mode identifies the etcd client used by this build.
const Mode = "custom"

// New creates the copied client with the etcd_leader_aware policy enabled.
func New(cfg clientv3.Config) (*clientv3.Client, error) {
	return clientv3.New(cfg.WithBalancer(clientv3.LeaderAwareBalancerName))
}
