//go:build !windows

package executor_test

import (
	"testing"
	"time"
)

const canSetBirth = false

func setBirth(*testing.T, string, time.Time) {}
