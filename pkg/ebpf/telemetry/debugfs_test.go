// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

package telemetry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testProfile = "./testdata/kprobe_profile"

func TestReadKprobeProfile(t *testing.T) {
	m, err := readKprobeProfile(testProfile)
	require.NoError(t, err)

	expected := map[string]KprobeStats{
		"r_tcp_sendmsg__7178":      {Hits: 1111389857, Misses: 0},
		"r_tcp_sendmsg__4256":      {Hits: 549926224, Misses: 0},
		"p_tcp_sendmsg__4256":      {Hits: 549925022, Misses: 0},
		"p_tcp_cleanup_rbuf__4256": {Hits: 0, Misses: 0},
		"p_tcp_close__4256":        {Hits: 540361567, Misses: 0},
		"r_tcp_close__4256":        {Hits: 540361465, Misses: 0},
		"p_tcp_set_state__4256":    {Hits: 2372974219, Misses: 155370519},
	}

	assert.Equal(t, expected, m)
}

func TestGetProbeStats(t *testing.T) {
	stats := getProbeStats(7178, testProfile)
	require.Equal(t, uint64(1111389857), stats["r_tcp_sendmsg_hits"])

	stats = getProbeStats(4256, testProfile)
	require.Equal(t, uint64(549926224), stats["r_tcp_sendmsg_hits"])
	require.Equal(t, uint64(549925022), stats["p_tcp_sendmsg_hits"])

	stats = getProbeStats(1, testProfile)
	require.Empty(t, stats)
}

func TestEventRegex(t *testing.T) {
	samples := []string{
		"r_tcp_sendmsg_net_7178",
		"r_tcp_sendmsg_http_4256",
		"p_tcp_sendmsg_security_4256",
		"p_tcp_set_state__4256",
	}

	uids := map[string]bool{
		"net":      true,
		"http":     true,
		"security": true,
		"":         true,
	}

	for _, event := range samples {
		parts := eventRegexp.FindStringSubmatch(event)
		require.Greater(t, len(parts), 3)

		uid := strings.ToLower(parts[2])
		_, ok := uids[uid]
		require.True(t, ok)
	}
}
