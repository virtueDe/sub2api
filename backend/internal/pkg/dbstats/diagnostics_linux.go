//go:build linux

package dbstats

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func systemSample() map[string]string {
	result := map[string]string{
		"proc_scope":            "mounted_proc; host_or_container_scope_unverified",
		"process_scope":         "application_self_only",
		"database_pid_scope":    "postgres_pid_namespace; not_correlated_with_application",
		"postgres_process_cpu":  "unavailable",
		"cgroup":                "not_sampled",
		"cpu_attribution":       "incomplete; use_host_sampler",
		"scheduler":             "unavailable",
		"scheduler_status":      "unavailable",
		"scheduler_source":      "cpu",
		"process_faults":        "unavailable",
		"process_faults_status": "unavailable",
		"process_faults_source": "process_cpu",
	}
	for _, item := range []struct{ name, path string }{{"cpu", "/proc/stat"}, {"memory", "/proc/meminfo"}, {"vmstat", "/proc/vmstat"}, {"cpu_psi", "/proc/pressure/cpu"}, {"memory_psi", "/proc/pressure/memory"}, {"io_psi", "/proc/pressure/io"}, {"diskstats", "/proc/diskstats"}, {"process_memory", "/proc/self/statm"}, {"process_status", "/proc/self/status"}, {"process_cpu", "/proc/self/stat"}} {
		started := time.Now()
		result[item.name+"_started_at"] = started.UTC().Format(time.RFC3339Nano)
		finish := func() {
			result[item.name+"_finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			result[item.name+"_elapsed_us"] = strconv.FormatInt(time.Since(started).Microseconds(), 10)
		}
		f, err := os.Open(item.path)
		if err != nil {
			finish()
			result[item.name] = "unavailable"
			result[item.name+"_status"] = "unavailable"
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 16385))
		_ = f.Close()
		finish()
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
			result["scheduler"] = selectKernelCounters(s, "scheduler", result[item.name+"_status"] == "truncated")
			result["scheduler_status"] = result[item.name+"_status"]
			s = strings.SplitN(s, "\n", 2)[0]
		case "vmstat":
			s = selectKernelCounters(s, "vmstat", result[item.name+"_status"] == "truncated")
		case "process_status":
			var lines []string
			for _, line := range strings.Split(s, "\n") {
				if strings.HasPrefix(line, "VmRSS:") || strings.HasPrefix(line, "RssAnon:") || strings.HasPrefix(line, "RssFile:") || strings.HasPrefix(line, "Threads:") || strings.HasPrefix(line, "NSpid:") {
					lines = append(lines, line)
				}
			}
			s = strings.Join(lines, "\n")
		case "process_cpu":
			s, result["process_faults"] = processCounters(s)
			if s != "unavailable" {
				result["process_faults_status"] = result[item.name+"_status"]
			}
		}
		result[item.name] = s
		if s == "unavailable" {
			result[item.name+"_status"] = "unavailable"
		}
	}
	return result
}

// Select complete numeric counter lines without inventing values for fields
// absent on this kernel. A truncated final line is not a complete counter.
func selectKernelCounters(raw, source string, truncated bool) string {
	if truncated {
		end := strings.LastIndexByte(raw, '\n')
		if end < 0 {
			return ""
		}
		raw = raw[:end+1]
	}
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := fields[0]
		keep := false
		switch source {
		case "scheduler":
			keep = name == "ctxt" || name == "processes" || name == "procs_running" || name == "procs_blocked"
		case "vmstat":
			keep = name == "pgfault" || name == "pgmajfault" || name == "pswpin" || name == "pswpout" || name == "oom_kill" || name == "allocstall" ||
				strings.HasPrefix(name, "allocstall_") || strings.HasPrefix(name, "pgscan_") || strings.HasPrefix(name, "pgsteal_") || strings.HasPrefix(name, "compact_") || strings.HasPrefix(name, "thp_")
		}
		if !keep {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		lines = append(lines, name+" "+fields[1])
	}
	return strings.Join(lines, "\n")
}

func processCounters(raw string) (cpu, faults string) {
	start, end := strings.IndexByte(raw, '('), strings.LastIndexByte(raw, ')')
	if start < 0 || end <= start {
		return "unavailable", "unavailable"
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) < 13 {
		return "unavailable", "unavailable"
	}
	for _, index := range []int{7, 9, 11, 12} {
		if _, err := strconv.ParseUint(fields[index], 10, 64); err != nil {
			return "unavailable", "unavailable"
		}
	}
	return "user_ticks=" + fields[11] + " system_ticks=" + fields[12], "minor_faults=" + fields[7] + " major_faults=" + fields[9]
}
