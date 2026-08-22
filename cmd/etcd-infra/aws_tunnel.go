package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file implements "aws tunnel": one SSM port-forwarding session per
// cluster member through the cluster's bastion (created by "aws up
// --bastion"). The sessions are outbound-only from the bastion's perspective
// and IAM-authenticated from the caller's, so etcd members need no inbound
// security-group rule from the host running the tunnels. The command prints
// the loopback endpoints, one CSV line on stdout, once every tunnel accepts
// connections, and holds the sessions until interrupted.
//
// The bastion is a pure TCP relay: it runs no test code, which is why "aws
// up" sizes it far below the members.

// awsTunnelReadyLine is emitted by session-manager-plugin once the local
// port accepts connections.
const awsTunnelReadyLine = "Waiting for connections"

// awsTunnelAttempts bounds retries when a freshly released loopback port is
// claimed by another process before session-manager-plugin binds it
// (inherent close-then-rebind race on a busy host).
const awsTunnelAttempts = 3

// awsTunnelStop releases a tunnel's process and port.
type awsTunnelStop func()

// startAWSSSMPortForward opens one tunnel with retries:
// 127.0.0.1:<free port> -> bastion (SSM target) -> host:remotePort. The
// returned endpoint is the loopback URL; call stop to end the session. The
// AWS CLI and session-manager-plugin must be in PATH.
func startAWSSSMPortForward(ctx context.Context, region, bastionID, host string, remotePort int, preferredPort ...int) (string, awsTunnelStop, error) {
	wantPort := 0
	if len(preferredPort) > 0 {
		wantPort = preferredPort[0]
	}
	if strings.TrimSpace(host) == "" {
		return "", nil, errors.New("remote host is required")
	}
	awsCLI, err := exec.LookPath("aws")
	if err != nil {
		return "", nil, errors.New("bastion access requires the AWS CLI in PATH")
	}
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		return "", nil, errors.New("bastion access requires session-manager-plugin in PATH (AWS Session Manager plugin)")
	}

	var lastErr error
	for attempt := 1; attempt <= awsTunnelAttempts; attempt++ {
		endpoint, stop, err := tryAWSSSMPortForward(ctx, awsCLI, region, bastionID, host, remotePort, wantPort)
		if err == nil {
			return endpoint, stop, nil
		}
		lastErr = err
	}
	return "", nil, fmt.Errorf("tunnel to %s:%d via bastion %s failed after %d attempts: %w",
		host, remotePort, bastionID, awsTunnelAttempts, lastErr)
}

// tryAWSSSMPortForward makes one tunnel attempt. On failure the plugin
// process is killed and reaped before returning, so retries start clean.
func tryAWSSSMPortForward(ctx context.Context, awsCLI, region, bastionID, host string, remotePort, preferredPort int) (string, awsTunnelStop, error) {
	// Reserve a free loopback port, then release it for the plugin to bind.
	// A preferred port lets a supervisor re-establish a dropped session on the
	// same port, keeping the endpoints stable for callers.
	bind := "127.0.0.1:0"
	if preferredPort > 0 {
		bind = fmt.Sprintf("127.0.0.1:%d", preferredPort)
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return "", nil, fmt.Errorf("reserve loopback port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return "", nil, fmt.Errorf("release loopback port: %w", err)
	}

	parameters := fmt.Sprintf(
		`{"host":[%q],"portNumber":["%d"],"localPortNumber":["%d"]}`,
		host, remotePort, port,
	)
	cmd := exec.CommandContext(ctx, awsCLI, "ssm", "start-session",
		"--region", region,
		"--target", bastionID,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", parameters,
	)
	// The CLI execs session-manager-plugin as its own child. A plain Kill
	// would orphan the plugin, which keeps the session AND the inherited
	// stdout pipe open — and Cmd.Wait then blocks forever waiting for the
	// pipe's readers. Run them in their own process group and kill the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("pipe session output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start ssm session: %w", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}

	// Keep draining stdout after readiness so the plugin never blocks on a
	// full pipe over a long-lived tunnel.
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		signaled := false
		for scanner.Scan() {
			if !signaled && strings.Contains(scanner.Text(), awsTunnelReadyLine) {
				signaled = true
				ready <- nil
			}
		}
		if signaled {
			return
		}
		if err := scanner.Err(); err != nil {
			ready <- err
			return
		}
		ready <- fmt.Errorf("session ended before the tunnel was ready: %s", strings.TrimSpace(stderr.String()))
	}()

	select {
	case err := <-ready:
		if err != nil {
			stop()
			return "", nil, err
		}
	case <-time.After(90 * time.Second):
		stop()
		return "", nil, fmt.Errorf("tunnel never became ready: %s", strings.TrimSpace(stderr.String()))
	case <-ctx.Done():
		stop()
		return "", nil, ctx.Err()
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), stop, nil
}

// runAWSTunnel opens one tunnel per cluster member through the bastion and
// holds them until interrupted. The single stdout line is the CSV of
// loopback client endpoints; progress goes to stderr so the script can
// capture stdout cleanly.
func runAWSTunnel(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws tunnel", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	statePath, err := awsStatePath(*name)
	if err != nil {
		return err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		return err
	}
	if state.Bastion == nil {
		return fmt.Errorf("cluster %s has no bastion; recreate it with 'etcd-infra aws up --bastion'", *name)
	}

	// Per-member tunnel handles; the supervisor swaps the stop function when
	// it re-establishes a dropped session.
	tunnels := make([]*supervisedTunnelHandle, 0, len(state.Instances))
	endpoints := make([]string, 0, len(state.Instances))
	for _, member := range state.Instances {
		fmt.Fprintf(os.Stderr, "opening tunnel to %s (%s) via bastion %s\n", member.Name, member.PrivateIPv4, state.Bastion.ID)
		endpoint, stop, err := startAWSSSMPortForward(ctx, state.Region, state.Bastion.ID, member.PrivateIPv4, 2379)
		if err != nil {
			return fmt.Errorf("tunnel to %s: %w", member.Name, err)
		}
		port := tunnelPort(endpoint)
		tun := &supervisedTunnelHandle{port: port, stop: stop, stopMtx: &sync.Mutex{}}
		tunnels = append(tunnels, tun)
		endpoints = append(endpoints, endpoint)
		// SSM sessions drop on network flaps and idle timeouts; a dead session
		// leaves the port refusing connections and every consumer fails. Watch
		// the port and re-establish on the same port so the endpoint stays
		// valid for the life of the command.
		go superviseAWSTunnel(ctx, state.Region, state.Bastion.ID, member, tun)
	}
	defer func() {
		for _, tun := range tunnels {
			tun.stopMtx.Lock()
			tun.stop()
			tun.stopMtx.Unlock()
		}
	}()

	fmt.Println(strings.Join(endpoints, ","))
	fmt.Fprintln(os.Stderr, "all tunnels ready; holding until interrupted")
	<-ctx.Done()
	return nil
}

// tunnelPort extracts the loopback port from a tunnel endpoint URL.
func tunnelPort(endpoint string) int {
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(endpoint, "http://"))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return port
}

// supervisedTunnelHandle is the per-member tunnel state shared with the
// supervisor; it swaps Stop when it re-establishes a dropped session.
type supervisedTunnelHandle struct {
	port    int
	stop    awsTunnelStop
	stopMtx *sync.Mutex
}

// superviseAWSTunnel watches a tunnel's loopback port and re-establishes the
// SSM session on the same port when the session dies. Probing uses a TCP
// connect: a dead plugin stops listening, which is exactly the failure mode.
func superviseAWSTunnel(ctx context.Context, region, bastionID string, member awsInstanceState, tun *supervisedTunnelHandle) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tun.port), time.Second)
		if err == nil {
			_ = conn.Close()
			continue
		}
		fmt.Fprintf(os.Stderr, "tunnel to %s (%s) is down; re-establishing\n", member.Name, member.PrivateIPv4)
		tun.stopMtx.Lock()
		tun.stop()
		endpoint, stop, err := startAWSSSMPortForward(ctx, region, bastionID, member.PrivateIPv4, 2379, tun.port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "re-establishing tunnel to %s failed: %v\n", member.Name, err)
			tun.stopMtx.Unlock()
			continue
		}
		if tunnelPort(endpoint) != tun.port {
			fmt.Fprintf(os.Stderr, "re-established tunnel to %s on a different port; endpoint drifted\n", member.Name)
		}
		tun.stop = stop
		tun.stopMtx.Unlock()
		fmt.Fprintf(os.Stderr, "tunnel to %s (%s) re-established\n", member.Name, member.PrivateIPv4)
	}
}
