package main

import (
	"fmt"
	"strings"
)

type clusterMember struct {
	Name      string
	ClientURL string
	PeerURL   string
}

func initialCluster(members []clusterMember) string {
	entries := make([]string, 0, len(members))
	for _, member := range members {
		entries = append(entries, member.Name+"="+member.PeerURL)
	}
	return strings.Join(entries, ",")
}

func etcdServerArgs(member clusterMember, members []clusterMember, token, dataDir string) []string {
	return []string{
		"--name", member.Name,
		"--data-dir", dataDir,
		"--listen-client-urls", "http://0.0.0.0:2379",
		"--advertise-client-urls", member.ClientURL,
		"--listen-peer-urls", "http://0.0.0.0:2380",
		"--initial-advertise-peer-urls", member.PeerURL,
		"--initial-cluster", initialCluster(members),
		"--initial-cluster-token", token,
		"--initial-cluster-state", "new",
		"--log-level", "warn",
	}
}

func validateMemberCount(count int) error {
	if count != 1 && count != 3 {
		return fmt.Errorf("members must be 1 or 3, got %d", count)
	}
	return nil
}
