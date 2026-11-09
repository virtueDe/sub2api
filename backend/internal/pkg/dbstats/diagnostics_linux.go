//go:build linux

package dbstats

import (
	"io"
	"os"
	"strings"
)

func systemSample() map[string]string {
	result := map[string]string{
		"proc_scope":           "mounted_proc; host_or_container_scope_unverified",
		"process_scope":        "application_self_only",
		"database_pid_scope":   "postgres_pid_namespace; not_correlated_with_application",
		"postgres_process_cpu": "unavailable",
		"cgroup":               "not_sampled",
		"cpu_attribution":      "incomplete; use_host_sampler",
	}
	for _, item := range []struct{ name, path string }{{"cpu", "/proc/stat"}, {"memory", "/proc/meminfo"}, {"cpu_psi", "/proc/pressure/cpu"}, {"memory_psi", "/proc/pressure/memory"}, {"io_psi", "/proc/pressure/io"}, {"diskstats", "/proc/diskstats"}, {"process_memory", "/proc/self/statm"}, {"process_status", "/proc/self/status"}, {"process_cpu", "/proc/self/stat"}} {
		f, err := os.Open(item.path)
		if err != nil {
			result[item.name] = "unavailable"
			result[item.name+"_status"] = "unavailable"
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 16385))
		_ = f.Close()
		if err != nil {
			result[item.name] = "unavailable"
			result[item.name+"_status"] = "unavailable"
			continue
		}
		result[item.name+"_status"] = "ok"
		if len(b) > 16384 {
			b = b[:16384]
			result[item.name+"_status"] = "truncated"
		}
		s := string(b)
		switch item.name {
		case "cpu":
			s = strings.SplitN(s, "\n", 2)[0]
		case "process_status":
			var lines []string
			for _, line := range strings.Split(s, "\n") {
				if strings.HasPrefix(line, "VmRSS:") || strings.HasPrefix(line, "RssAnon:") || strings.HasPrefix(line, "RssFile:") || strings.HasPrefix(line, "Threads:") || strings.HasPrefix(line, "NSpid:") {
					lines = append(lines, line)
				}
			}
			s = strings.Join(lines, "\n")
		case "process_cpu":
			end := strings.LastIndex(s, ")")
			if end < 0 {
				s = "unavailable"
				break
			}
			fields := strings.Fields(s[end+1:])
			if len(fields) < 13 {
				s = "unavailable"
				break
			}
			s = "user_ticks=" + fields[11] + " system_ticks=" + fields[12]
		}
		result[item.name] = s
		if s == "unavailable" {
			result[item.name+"_status"] = "unavailable"
		}
	}
	return result
}
