package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Peer-bytes metrics. etcd_network_peer_sent_bytes_total counts serialized
// Raft messages rather than TCP/TLS framing, which makes it the causal
// server-side measure of the redundant follower-to-leader proposal copy that
// leader-aware client routing avoids; summing received bytes too would
// double-count it. The counters are served on the client port's /metrics
// endpoint when --listen-metrics-urls is unset, so they are reachable through
// the same bastion tunnels (AWS) or host ports (local) as client traffic.
const (
	peerSentBytesMetric     = "etcd_network_peer_sent_bytes_total"
	peerReceivedBytesMetric = "etcd_network_peer_received_bytes_total"
)

// scrapePeerMetric sums the named counter across all label series of one
// member's /metrics endpoint.
func scrapePeerMetric(ctx context.Context, httpClient *http.Client, endpoint, metric string) (float64, error) {
	metricsURL := strings.TrimRight(endpoint, "/") + "/metrics"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return 0, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metrics returned %s", response.Status)
	}

	var total float64
	series := 0
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, metric+"{") && !strings.HasPrefix(line, metric+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed %s sample %q", metric, line)
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s sample %q: %w", metric, line, err)
		}
		total += value
		series++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if series == 0 {
		return 0, fmt.Errorf("metrics omitted %s", metric)
	}
	return total, nil
}

// scrapePeerBytes snapshots both peer-byte counters for every endpoint.
func scrapePeerBytes(ctx context.Context, endpoints []string) (map[string][2]float64, error) {
	values := make(map[string][2]float64, len(endpoints))
	httpClient := &http.Client{Timeout: 5 * time.Second}
	for _, endpoint := range endpoints {
		sent, err := scrapePeerMetric(ctx, httpClient, endpoint, peerSentBytesMetric)
		if err != nil {
			return nil, fmt.Errorf("scrape peer-sent bytes from %s: %w", endpoint, err)
		}
		received, err := scrapePeerMetric(ctx, httpClient, endpoint, peerReceivedBytesMetric)
		if err != nil {
			return nil, fmt.Errorf("scrape peer-received bytes from %s: %w", endpoint, err)
		}
		values[endpoint] = [2]float64{sent, received}
	}
	return values, nil
}

// runMetrics prints one snapshot of the peer-byte counters per endpoint plus
// totals. Diff two snapshots around a run to measure the run's peer traffic;
// the sent-bytes delta is the bandwidth the client routing consumed.
func runMetrics(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("metrics", flag.ContinueOnError)
	endpoints := flags.String("endpoints", defaultEndpoint, "comma-separated etcd endpoints")
	if err := flags.Parse(args); err != nil {
		return err
	}

	values, err := scrapePeerBytes(ctx, splitEndpoints(*endpoints))
	if err != nil {
		return err
	}

	var totalSent, totalReceived float64
	fmt.Printf("%-40s %20s %20s\n", "ENDPOINT", "PEER_SENT_BYTES", "PEER_RECV_BYTES")
	for _, endpoint := range splitEndpoints(*endpoints) {
		pair := values[endpoint]
		totalSent += pair[0]
		totalReceived += pair[1]
		fmt.Printf("%-40s %20.0f %20.0f\n", endpoint, pair[0], pair[1])
	}
	fmt.Printf("%-40s %20.0f %20.0f\n", "TOTAL", totalSent, totalReceived)
	return nil
}
