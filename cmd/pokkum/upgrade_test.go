package main

import (
	"context"
	"testing"
)

func TestUpgradeCommand_CheckOffline(t *testing.T) {
	flags := &upgradeFlags{
		check:   true,
		output:  "json",
		offline: true,
	}

	err := runUpgrade(context.Background(), discardLogger(), flags)
	if err != nil {
		t.Fatalf("runUpgrade failed: %v", err)
	}
}
