// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"sync"
	"testing"
	"testing/synctest"
)

// testCache returns a cache whose guest at testAddr is up.
func testCache(t *testing.T, n *guestNet) *ChannelCache {
	t.Helper()
	d := testDialer(t, n)
	bootFor(t, n, d, testAddr)
	c := NewChannelCache(d)
	t.Cleanup(func() { c.close(testAddr) })
	return c
}

func runOn(c *ChannelCache, argv ...string) (int, error) {
	return c.run(context.Background(), testAddr, Command{Argv: argv}, io.Discard)
}

func TestChannelCache_EvictOnlyDropsTheStaleChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newGuestNet(t)
		c := testCache(t, n)
		ctx := context.Background()
		first, err := c.get(ctx, testAddr)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		c.evict(testAddr, first)
		if _, err := c.get(ctx, testAddr); err != nil {
			t.Fatalf("get after evict: %v", err)
		}
		// A command failing late on the stale channel must not close the
		// replacement.
		c.evict(testAddr, first)
		if code, err := runOn(c, "true"); code != 0 || err != nil {
			t.Errorf("run after a stale evict = (%d, %v); want the replacement to serve it", code, err)
		}
		synctest.Wait()
		if dials, open := n.dialCount(), n.open(testAddr); dials != 2 || open != 1 {
			t.Errorf("%d dials, %d open; want the replacement as the only connection", dials, open)
		}
		c.close(testAddr)
		synctest.Wait()
		if open := n.open(testAddr); open != 0 {
			t.Errorf("%d connections open after close; want none", open)
		}
	})
}

func TestChannelCache_EvictsOnTransportFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newGuestNet(t)
		c := testCache(t, n)
		if _, err := runOn(c, "x"); err != nil {
			t.Fatalf("run: %v", err)
		}
		n.drop()
		synctest.Wait()
		if _, err := runOn(c, "x"); !isTransport(err) {
			t.Errorf("run on a dropped connection = %v; want the transport failure reported, not retried", err)
		}
		n.setRun(func(context.Context, Command, io.Writer, io.Writer) guestExit { return guestExit{status: 3} })
		if code, err := runOn(c, "x"); code != 3 || err != nil {
			t.Errorf("run = (%d, %v); want exit 3 over a fresh connection", code, err)
		}
		if dials := n.dialCount(); dials != 2 {
			t.Errorf("%d dials; want one redial", dials)
		}
	})
}

func TestChannelCache_KeepsTheChannelWhenASessionIsRefused(t *testing.T) {
	n := newGuestNet(t)
	c := testCache(t, n)
	if _, err := runOn(c, "x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	n.setRefuseOpen(true)
	if _, err := runOn(c, "x"); err == nil || isTransport(err) {
		t.Errorf("run with sessions refused = %v; want a refusal that is not a transport failure", err)
	}
	n.setRefuseOpen(false)
	if _, err := runOn(c, "x"); err != nil {
		t.Errorf("run once sessions open again = %v; want success", err)
	}
	if dials := n.dialCount(); dials != 1 {
		t.Errorf("%d dials; want the one connection kept throughout", dials)
	}
}

func TestChannelCache_ConcurrentCommandsShareAConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newGuestNet(t)
		c := testCache(t, n)
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if _, err := runOn(c, "x"); err != nil {
					t.Errorf("run: %v", err)
				}
			})
		}
		wg.Wait()
		synctest.Wait()
		if open := n.open(testAddr); open != 1 {
			t.Errorf("%d connections open; want the losers of the dial race closed", open)
		}
	})
}
