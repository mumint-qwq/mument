//go:build linux

package preview

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpaceContainerLimits(t *testing.T) {
	if os.Getenv("MUMENT_SPACE_CONTAINER_TEST") != "1" {
		t.Skip("run inside the fixed limits container")
	}
	read := func(name string) string {
		data, err := os.ReadFile("/sys/fs/cgroup/" + name)
		require.NoError(t, err)
		return strings.TrimSpace(string(data))
	}
	mem, swap, cpu := read("memory.max"), read("memory.swap.max"), strings.Fields(read("cpu.max"))
	require.Equal(t, strconv.Itoa(1<<30), mem)
	require.Equal(t, strconv.Itoa(1<<30), swap)
	require.Len(t, cpu, 2)
	quota, err := strconv.ParseInt(cpu[0], 10, 64)
	require.NoError(t, err)
	period, err := strconv.ParseInt(cpu[1], 10, 64)
	require.NoError(t, err)
	require.Equal(t, 2*period, quota)
	events := read("memory.events")
	require.Contains(t, "\n"+events+"\n", "\noom 0\n")
	require.Contains(t, "\n"+events+"\n", "\noom_kill 0\n")
	t.Logf("cpu.max=%s memory.max=%s memory.swap.max=%s memory.events=%s", strings.Join(cpu, " "), mem, swap, events)
}
