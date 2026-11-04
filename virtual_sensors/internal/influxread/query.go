// Package influxread is a minimal, READ-ONLY InfluxDB 2.x Flux query
// client for the `backfill` CLI subcommand - just enough to reproduce
// fetch.py's queries (see the hot-water-estimate Python project) without
// pulling in the full influxdb-client-go dependency tree. It issues only
// `from(...) |> range(...) |> filter(...)` queries over HTTP POST and
// parses the annotated-CSV response; it never writes.
package influxread

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a read-only Flux query client for one InfluxDB server/org.
type Client struct {
	URL   string // e.g. http://192.168.0.36:8086
	Org   string
	Token string // never logged, never put on a command line - see cmd/virtual_sensors
}

// Point is one (time, value, measurement-unit) row - measurement carries
// the unit tag ("W" or "kW") exactly like fetch.py relies on.
type Point struct {
	Time        time.Time
	Value       float64
	Measurement string
}

// QueryEntity runs the equivalent of:
//
//	from(bucket: bucket) |> range(start: from, stop: to)
//	  |> filter(fn: (r) => r.entity_id == entityID and r.domain == "sensor")
//	  |> filter(fn: (r) => r._field == "value")
//
// and returns every point, oldest first. Read-only: never issues a write
// or delete query.
func (c *Client) QueryEntity(ctx context.Context, bucket, entityID string, from, to time.Time) ([]Point, error) {
	flux := fmt.Sprintf(`from(bucket: %q)
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r.entity_id == %q)
  |> filter(fn: (r) => r._field == "value")
  |> keep(columns: ["_time", "_value", "_measurement"])
  |> sort(columns: ["_time"])`,
		bucket, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), entityID)
	return c.run(ctx, flux)
}

func (c *Client) run(ctx context.Context, flux string) ([]Point, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.URL+"/api/v2/query?org="+c.Org, strings.NewReader(flux))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+c.Token)
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query influxdb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("influxdb query returned %s: %s", resp.Status, string(body))
	}
	return parseAnnotatedCSV(resp.Body)
}

// parseAnnotatedCSV parses InfluxDB's annotated-CSV Flux response: lines
// starting with '#' are annotations, a blank line separates tables, and
// each table has its own header row. We only need _time/_value/_measurement.
func parseAnnotatedCSV(r io.Reader) ([]Point, error) {
	var points []Point
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var header []string
	timeIdx, valueIdx, measIdx := -1, -1, -1

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			header = nil // next non-blank, non-# line is a fresh table header
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if header == nil {
			header = fields
			timeIdx, valueIdx, measIdx = -1, -1, -1
			for i, h := range header {
				switch h {
				case "_time":
					timeIdx = i
				case "_value":
					valueIdx = i
				case "_measurement":
					measIdx = i
				}
			}
			continue
		}
		if timeIdx < 0 || valueIdx < 0 || timeIdx >= len(fields) || valueIdx >= len(fields) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, fields[timeIdx])
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(fields[valueIdx], 64)
		if err != nil {
			continue // non-numeric field (e.g. a string-field row); skip
		}
		meas := ""
		if measIdx >= 0 && measIdx < len(fields) {
			meas = fields[measIdx]
		}
		points = append(points, Point{Time: t, Value: v, Measurement: meas})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// ToWatts converts a point's value to watts using its own _measurement
// tag ("kW" -> x1000), exactly like build.py's load_series does per-row
// rather than assuming a fixed unit per entity (see the grid_meter_power
// unit-glitch episode documented in the hot-water-estimate README.md).
func (p Point) ToWatts() float64 {
	if p.Measurement == "kW" {
		return p.Value * 1000.0
	}
	return p.Value
}
