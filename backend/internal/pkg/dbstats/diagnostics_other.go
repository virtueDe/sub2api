//go:build !linux

package dbstats

func systemSample() map[string]string {
	return map[string]string{"visibility": "Linux /proc sampling unavailable", "postgres_process_cpu": "unavailable", "cgroup": "not_sampled", "cpu_attribution": "incomplete"}
}
