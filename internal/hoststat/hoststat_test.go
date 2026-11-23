package hoststat

import "testing"

func TestParsers(t *testing.T) {
	l1, l5, l15, err := ParseLoadavg("8.23 10.78 7.47 6/1257 129003\n")
	if err != nil || l1 != 8.23 || l5 != 10.78 || l15 != 7.47 {
		t.Fatalf("loadavg %v %v %v %v", l1, l5, l15, err)
	}
	times, cpus, err := ParseStat("cpu  431070 1332 102209 5178329 17809 0 4237 0 0 0\ncpu0 1 2 3 4 5 6 7 8 0 0\ncpu1 1 2 3 4 5 6 7 8 0 0\nbtime 1\n")
	if err != nil || cpus != 2 || times.total != 5734986 || times.busy != 538848 {
		t.Fatalf("stat %+v %d %v", times, cpus, err)
	}
	used, total, err := ParseMeminfo("MemTotal:       15491836 kB\nMemFree:         5085800 kB\nMemAvailable:   11901988 kB\n")
	if err != nil || total != 15491836*1024 || used != (15491836-11901988)*1024 {
		t.Fatalf("meminfo %d %d %v", used, total, err)
	}
}
