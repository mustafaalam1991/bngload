package clock

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultChronyConf is the chrony configuration path on Ubuntu.
const DefaultChronyConf = "/etc/chrony/chrony.conf"

// Makestep is chrony's "makestep threshold limit" directive. The clock may
// be stepped when the offset exceeds Threshold seconds, but only during the
// first Limit updates; a negative Limit allows steps at any time, which MR-2
// forbids because a step during a run corrupts measured intervals.
type Makestep struct {
	ThresholdS float64 `json:"threshold_s"`
	Limit      int     `json:"limit"`
}

// ChronyConf is the subset of chrony configuration relevant to MR-2/MR-5.
type ChronyConf struct {
	Path           string    `json:"path"`
	Found          bool      `json:"found"`
	Files          []string  `json:"files_parsed"`
	MaxSlewRatePPM *float64  `json:"maxslewrate_ppm"`
	Makestep       *Makestep `json:"makestep"`
	HWTimestamp    []string  `json:"hwtimestamp"`
	Refclocks      []string  `json:"refclocks"`
	Sources        []string  `json:"sources"`
}

// ParseChronyConf reads path and every file pulled in by "confdir" and
// "include" directives (Ubuntu ships /etc/chrony/conf.d). Unknown directives
// are ignored. A missing file returns Found=false and no error.
func ParseChronyConf(path string) (ChronyConf, error) {
	c := ChronyConf{Path: path}
	if _, err := os.Stat(path); err != nil {
		return c, nil
	}
	c.Found = true
	seen := map[string]bool{}
	var parse func(string) error
	parse = func(p string) error {
		if seen[p] {
			return nil
		}
		seen[p] = true
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		c.Files = append(c.Files, p)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if i := strings.IndexAny(line, "#!;%"); i == 0 {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "maxslewrate":
				if len(fields) > 1 {
					if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
						c.MaxSlewRatePPM = &v
					}
				}
			case "makestep":
				if len(fields) > 2 {
					th, e1 := strconv.ParseFloat(fields[1], 64)
					lim, e2 := strconv.Atoi(fields[2])
					if e1 == nil && e2 == nil {
						c.Makestep = &Makestep{ThresholdS: th, Limit: lim}
					}
				}
			case "hwtimestamp":
				c.HWTimestamp = append(c.HWTimestamp, strings.Join(fields[1:], " "))
			case "refclock":
				c.Refclocks = append(c.Refclocks, strings.Join(fields[1:], " "))
			case "server", "pool", "peer":
				c.Sources = append(c.Sources, strings.Join(fields, " "))
			case "confdir", "sourcedir":
				for _, dir := range fields[1:] {
					matches, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
					if fields[0] == "sourcedir" {
						more, _ := filepath.Glob(filepath.Join(dir, "*.sources"))
						matches = append(matches, more...)
					}
					sort.Strings(matches)
					for _, m := range matches {
						_ = parse(m)
					}
				}
			case "include":
				if len(fields) > 1 {
					matches, _ := filepath.Glob(fields[1])
					sort.Strings(matches)
					for _, m := range matches {
						_ = parse(m)
					}
				}
			}
		}
		return sc.Err()
	}
	err := parse(path)
	return c, err
}

// chronyTrackingFields are the columns of `chronyc -c tracking`.
var chronyTrackingFields = []string{
	"reference_id", "reference_name", "stratum", "reference_time",
	"system_time_s", "last_offset_s", "rms_offset_s", "frequency_ppm",
	"residual_freq_ppm", "skew_ppm", "root_delay_s", "root_dispersion_s",
	"update_interval_s", "leap_status",
}

// ChronyTracking runs `chronyc -c tracking` and returns its fields by name.
// It returns nil when chronyc is not installed or chronyd is not running.
func ChronyTracking() map[string]string {
	path, err := exec.LookPath("chronyc")
	if err != nil {
		return nil
	}
	cmd := exec.Command(path, "-c", "tracking")
	done := make(chan struct{})
	var out []byte
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return map[string]string{"error": "chronyc timed out"}
	}
	if err != nil {
		return map[string]string{"error": strings.TrimSpace(err.Error())}
	}
	return parseTrackingCSV(strings.TrimSpace(string(out)))
}

func parseTrackingCSV(line string) map[string]string {
	parts := strings.Split(line, ",")
	m := map[string]string{}
	if len(parts) != len(chronyTrackingFields) {
		m["raw"] = line
		return m
	}
	for i, f := range chronyTrackingFields {
		m[f] = parts[i]
	}
	return m
}
