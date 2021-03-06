package cmd

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/geckoboard/cli-table"
	"github.com/geckoboard/prism/profiler"
	"gopkg.in/urfave/cli.v1"
)

func TestCorrelateEntries(t *testing.T) {
	p1 := &profiler.Profile{
		Target: &profiler.CallMetrics{
			FnName: "main",
			NestedCalls: []*profiler.CallMetrics{
				{
					FnName: "foo",
					NestedCalls: []*profiler.CallMetrics{
						{
							FnName:      "bar",
							NestedCalls: []*profiler.CallMetrics{},
						},
					},
				},
			},
		},
	}

	p2 := &profiler.Profile{
		Target: &profiler.CallMetrics{
			FnName: "main",
			NestedCalls: []*profiler.CallMetrics{
				{
					FnName:      "bar",
					NestedCalls: []*profiler.CallMetrics{},
				},
			},
		},
	}

	profileList := []*profiler.Profile{p1, p2}
	correlations := prepareCorrelationData(profileList[0], len(profileList))
	for profileIndex := 1; profileIndex < len(profileList); profileIndex++ {
		correlations, _ = correlateMetric(profileIndex, profileList[profileIndex].Target, 0, correlations)
	}

	expCount := 3
	if len(correlations) != expCount {
		t.Fatalf("expected correlation table to contain %d entries; got %d", expCount, len(correlations))
	}

	specs := []struct {
		FnName      string
		LeftNotNil  bool
		RightNotNil bool
	}{
		{"main", true, true},
		{"foo", true, false},
		{"bar", true, true},
	}

	for specIndex, spec := range specs {
		row := correlations[specIndex]
		if len(row.metrics) != len(profileList) {
			t.Errorf("[spec %d] expected metric count for correlation row to be %d; got %d", specIndex, len(profileList), len(row.metrics))
			continue
		}

		if row.fnName != spec.FnName {
			t.Errorf("[spec %d] expected correlation row fnName to be %q; got %q", specIndex, spec.FnName, row.fnName)
			continue
		}

		if (spec.LeftNotNil && row.metrics[0] == nil) || (!spec.LeftNotNil && row.metrics[0] != nil) {
			t.Errorf("[spec %d] left correlation entry mismatch; expected it not to be nil? %t", specIndex, spec.LeftNotNil)
			continue
		}
		if (spec.RightNotNil && row.metrics[1] == nil) || (!spec.RightNotNil && row.metrics[1] != nil) {
			t.Errorf("[spec %d] right correlation entry mismatch; expected it not to be nil? %t", specIndex, spec.RightNotNil)
			continue
		}
	}
}

func TestDiffWithProfileLabel(t *testing.T) {
	profileDir, profileFiles := mockProfiles(t, true)
	defer os.RemoveAll(profileDir)

	// Mock args
	set := flag.NewFlagSet("test", 0)
	set.String("display-columns", SupportedColumnNames(), "")
	set.String("display-unit", "ns", "")
	set.Float64("display-threshold", 10.0, "")
	set.Parse(profileFiles)
	ctx := cli.NewContext(nil, set, nil)

	// Redirect stdout
	stdOut := os.Stdout
	pRead, pWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pWrite

	// Run diff and capture output
	err = DiffProfiles(ctx)
	if err != nil {
		os.Stdout = stdOut
		t.Fatal(err)
	}

	// Drain pipe and restore stdout
	var buf bytes.Buffer
	pWrite.Close()
	io.Copy(&buf, pRead)
	pRead.Close()
	os.Stdout = stdOut

	output := buf.String()
	expOutput := `+------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------+----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
|            | With Label - baseline                                                                                                                                                   | With Label                                                                                                                                                                                                                                                                 |
+------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------+----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
| call stack |          total |            min |            max |           mean |         median | invoc |            p50 |            p75 |            p90 |            p99 | stddev |                     total |                       min |                       max |                      mean |                    median | invoc |                       p50 |                       p75 |                       p90 |                       p99 | stddev |
+------------+----------------+----------------+----------------+----------------+----------------+-------+----------------+----------------+----------------+----------------+--------+---------------------------+---------------------------+---------------------------+---------------------------+---------------------------+-------+---------------------------+---------------------------+---------------------------+---------------------------+--------+
| - main     | 120,000,000 ns | 120,000,000 ns | 120,000,000 ns | 120,000,000 ns | 120,000,000 ns |     1 | 120,000,000 ns | 120,000,000 ns | 120,000,000 ns | 120,000,000 ns |  0.000 | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) |     1 | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) | 10,000,000 ns (↓ 1100.0%) |  0.000 |
| | + foo    | 120,000,000 ns |  10,000,000 ns | 110,000,000 ns |  60,000,000 ns |  60,000,000 ns |     2 |  10,000,000 ns |  10,000,000 ns |  10,000,000 ns | 120,000,000 ns | 70.711 | 10,000,000 ns (↓ 1100.0%) |  4,000,000 ns  (↓ 150.0%) |  6,000,000 ns (↓ 1733.3%) |  5,000,000 ns (↓ 1100.0%) |  5,000,000 ns (↓ 1100.0%) |     2 |  4,000,000 ns  (↓ 150.0%) |  4,000,000 ns  (↓ 150.0%) |  4,000,000 ns  (↓ 150.0%) |  6,000,000 ns (↓ 1900.0%) |  1.414 |
+------------+----------------+----------------+----------------+----------------+----------------+-------+----------------+----------------+----------------+----------------+--------+---------------------------+---------------------------+---------------------------+---------------------------+---------------------------+-------+---------------------------+---------------------------+---------------------------+---------------------------+--------+
`

	if expOutput != output {
		t.Fatalf("tabularized diff output mismatch; expected:\n%s\n\ngot:\n%s", expOutput, output)
	}
}

func TestDiffWithProfileLabelAndAutoUnitDetection(t *testing.T) {
	profileDir, profileFiles := mockProfiles(t, true)
	defer os.RemoveAll(profileDir)

	// Mock args
	set := flag.NewFlagSet("test", 0)
	set.String("display-columns", SupportedColumnNames(), "")
	set.String("display-unit", "auto", "")
	set.Float64("display-threshold", 10.0, "")
	set.Parse(profileFiles)
	ctx := cli.NewContext(nil, set, nil)

	// Redirect stdout
	stdOut := os.Stdout
	pRead, pWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pWrite

	// Run diff and capture output
	err = DiffProfiles(ctx)
	if err != nil {
		os.Stdout = stdOut
		t.Fatal(err)
	}

	// Drain pipe and restore stdout
	var buf bytes.Buffer
	pWrite.Close()
	io.Copy(&buf, pRead)
	pRead.Close()
	os.Stdout = stdOut

	output := buf.String()
	expOutput := `+------------+----------------------------------------------------------------------------------------------------------------------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
|            | With Label - baseline                                                                                                      | With Label                                                                                                                                                                                                                    |
+------------+----------------------------------------------------------------------------------------------------------------------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
| call stack |     total |       min |       max |      mean |    median | invoc |       p50 |       p75 |       p90 |       p99 | stddev |                total |                  min |                  max |                 mean |               median | invoc |                  p50 |                  p75 |                  p90 |                  p99 | stddev |
+------------+-----------+-----------+-----------+-----------+-----------+-------+-----------+-----------+-----------+-----------+--------+----------------------+----------------------+----------------------+----------------------+----------------------+-------+----------------------+----------------------+----------------------+----------------------+--------+
| - main     | 120.00 ms | 120.00 ms | 120.00 ms | 120.00 ms | 120.00 ms |     1 | 120.00 ms | 120.00 ms | 120.00 ms | 120.00 ms |  0.000 | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) |     1 | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) | 10.00 ms (↓ 1100.0%) |  0.000 |
| | + foo    | 120.00 ms |  10.00 ms | 110.00 ms |  60.00 ms |  60.00 ms |     2 |  10.00 ms |  10.00 ms |  10.00 ms | 120.00 ms | 70.711 | 10.00 ms (↓ 1100.0%) |  4.00 ms        (--) |  6.00 ms (↓ 1733.3%) |  5.00 ms (↓ 1100.0%) |  5.00 ms (↓ 1100.0%) |     2 |  4.00 ms        (--) |  4.00 ms        (--) |  4.00 ms        (--) |  6.00 ms (↓ 1900.0%) |  1.414 |
+------------+-----------+-----------+-----------+-----------+-----------+-------+-----------+-----------+-----------+-----------+--------+----------------------+----------------------+----------------------+----------------------+----------------------+-------+----------------------+----------------------+----------------------+----------------------+--------+
`

	if expOutput != output {
		t.Fatalf("tabularized diff output mismatch; expected:\n%s\n\ngot:\n%s", expOutput, output)
	}
}

func TestDiffWithoutProfileLabel(t *testing.T) {
	profileDir, profileFiles := mockProfiles(t, false)
	defer os.RemoveAll(profileDir)

	// Mock args
	set := flag.NewFlagSet("test", 0)
	set.String("display-columns", SupportedColumnNames(), "")
	set.String("display-unit", "us", "")
	set.Float64("display-threshold", 4.0, "")
	set.Parse(profileFiles)
	ctx := cli.NewContext(nil, set, nil)

	// Redirect stdout
	stdOut := os.Stdout
	pRead, pWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pWrite

	// Restore stdout incase of a panic
	defer func() {
		os.Stdout = stdOut
	}()

	// Run diff and capture output
	err = DiffProfiles(ctx)
	if err != nil {
		os.Stdout = stdOut
		t.Fatal(err)
	}

	// Drain pipe and restore stdout
	var buf bytes.Buffer
	pWrite.Close()
	io.Copy(&buf, pRead)
	pRead.Close()
	os.Stdout = stdOut

	output := buf.String()
	expOutput := `+------------+----------------------------------------------------------------------------------------------------------------------------------------------------------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
|            | baseline                                                                                                                                                       | profile 1                                                                                                                                                                                                                                                         |
+------------+----------------------------------------------------------------------------------------------------------------------------------------------------------------+-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------+
| call stack |         total |           min |           max |          mean |        median | invoc |           p50 |           p75 |           p90 |           p99 | stddev |                    total |                      min |                      max |                     mean |                   median | invoc |                      p50 |                      p75 |                      p90 |                      p99 | stddev |
+------------+---------------+---------------+---------------+---------------+---------------+-------+---------------+---------------+---------------+---------------+--------+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+-------+--------------------------+--------------------------+--------------------------+--------------------------+--------+
| - main     | 120,000.00 us | 120,000.00 us | 120,000.00 us | 120,000.00 us | 120,000.00 us |     1 | 120,000.00 us | 120,000.00 us | 120,000.00 us | 120,000.00 us |  0.000 | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) |     1 | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) | 10,000.00 us (↓ 1100.0%) |  0.000 |
| | + foo    | 120,000.00 us |  10,000.00 us | 110,000.00 us |  60,000.00 us |  60,000.00 us |     2 |  10,000.00 us |  10,000.00 us |  10,000.00 us | 120,000.00 us | 70.711 | 10,000.00 us (↓ 1100.0%) |  4,000.00 us  (↓ 150.0%) |  6,000.00 us (↓ 1733.3%) |  5,000.00 us (↓ 1100.0%) |  5,000.00 us (↓ 1100.0%) |     2 |  4,000.00 us  (↓ 150.0%) |  4,000.00 us  (↓ 150.0%) |  4,000.00 us  (↓ 150.0%) |  6,000.00 us (↓ 1900.0%) |  1.414 |
+------------+---------------+---------------+---------------+---------------+---------------+-------+---------------+---------------+---------------+---------------+--------+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+-------+--------------------------+--------------------------+--------------------------+--------------------------+--------+
`

	if expOutput != output {
		t.Fatalf("tabularized diff output mismatch; expected:\n%s\n\ngot:\n%s", expOutput, output)
	}
}

func TestFmtDiff(t *testing.T) {
	specs := []struct {
		before        time.Duration
		after         time.Duration
		clipThreshold float64
		expOut        string
	}{
		{1 * time.Millisecond, 1 * time.Millisecond, 0.0, "1.00 ms (" + cYellow + string(approxEqualSymbol) + cReset + ")"},
		{2 * time.Millisecond, 4 * time.Millisecond, 0.0, "4.00 ms (" + cRed + string(greaterThanSymbol) + " 100.0%" + cReset + ")"},
		{10 * time.Millisecond, 8 * time.Millisecond, 0, "8.00 ms (" + cGreen + string(lessThanSymbol) + " 25.0%" + cReset + ")"},
		{10 * time.Millisecond, 0 * time.Millisecond, 0, "0.00 ms (--)"},
		{0 * time.Millisecond, 10 * time.Millisecond, 0, "10.00 ms (--)"},
		{1 * time.Millisecond, 10 * time.Millisecond, 11.0, "10.00 ms (--)"},
	}

	dp := &diffPrinter{
		unit: displayUnitMs,
	}

	for specIndex, spec := range specs {
		before := &profiler.CallMetrics{TotalTime: spec.before}
		after := &profiler.CallMetrics{TotalTime: spec.after}
		dp.clipThreshold = spec.clipThreshold

		out := dp.fmtDiff(before, after, tableColTotal)
		if out != spec.expOut {
			t.Errorf("[spec %d] expected formatted output to be %q; got %q", specIndex, spec.expOut, out)
		}
	}
}

func TestAlignAndAppendRows(t *testing.T) {
	dp := &diffPrinter{
		rows: [][]string{
			[]string{"Just data", "100 ms (▲ 1020.0x)", "1000000 ms (▲ 123456.0x)", "1 ms (▲ 500.0x)"},
			[]string{"Just data", "100 ms (^ 12.0x)", "100 ms (--)", "1 ms (▼ 500.0x)"},
			[]string{"Just data", "100 ms (V 0.9x)", "134 ms (V 126.0x)", "1 ms (--)"},
		},
	}

	var buf bytes.Buffer
	ta := table.New(4)
	ta.SetPadding(1)
	ta.SetHeader(0, "A", table.AlignRight)
	ta.SetHeader(1, "B", table.AlignRight)
	ta.SetHeader(2, "C", table.AlignRight)
	ta.SetHeader(3, "D", table.AlignRight)
	dp.alignAndAppendRows(ta)
	ta.Write(&buf, table.PreserveAnsi)
	tableOutput := buf.String()

	expOutput := `+-----------+--------------------+--------------------------+-----------------+
|         A |                  B |                        C |               D |
+-----------+--------------------+--------------------------+-----------------+
| Just data | 100 ms (▲ 1020.0x) | 1000000 ms (▲ 123456.0x) | 1 ms (▲ 500.0x) |
| Just data | 100 ms   (^ 12.0x) |     100 ms          (--) | 1 ms (▼ 500.0x) |
| Just data | 100 ms    (V 0.9x) |     134 ms    (V 126.0x) | 1 ms       (--) |
+-----------+--------------------+--------------------------+-----------------+
`

	if tableOutput != expOutput {
		t.Fatalf("expected output to be:\n%s\ngot:\n%s\n", expOutput, tableOutput)
	}
}

func mockProfiles(t *testing.T, useLabel bool) (profileDir string, profileFiles []string) {
	label := ""
	if useLabel {
		label = "With Label"
	}
	profiles := []*profiler.Profile{
		&profiler.Profile{
			Label: label,
			Target: &profiler.CallMetrics{
				FnName:      "main",
				TotalTime:   120 * time.Millisecond,
				MinTime:     120 * time.Millisecond,
				MeanTime:    120 * time.Millisecond,
				MaxTime:     120 * time.Millisecond,
				MedianTime:  120 * time.Millisecond,
				P50Time:     120 * time.Millisecond,
				P75Time:     120 * time.Millisecond,
				P90Time:     120 * time.Millisecond,
				P99Time:     120 * time.Millisecond,
				StdDev:      0.0,
				Invocations: 1,
				NestedCalls: []*profiler.CallMetrics{
					{
						FnName:      "foo",
						TotalTime:   120 * time.Millisecond,
						MeanTime:    60 * time.Millisecond,
						MedianTime:  60 * time.Millisecond,
						MinTime:     10 * time.Millisecond,
						MaxTime:     110 * time.Millisecond,
						P50Time:     10 * time.Millisecond,
						P75Time:     10 * time.Millisecond,
						P90Time:     10 * time.Millisecond,
						P99Time:     120 * time.Millisecond,
						StdDev:      70.71068,
						Invocations: 2,
					},
				},
			},
		},
		&profiler.Profile{
			Label: label,
			Target: &profiler.CallMetrics{
				FnName:      "main",
				TotalTime:   10 * time.Millisecond,
				MinTime:     10 * time.Millisecond,
				MeanTime:    10 * time.Millisecond,
				MaxTime:     10 * time.Millisecond,
				MedianTime:  10 * time.Millisecond,
				P50Time:     10 * time.Millisecond,
				P75Time:     10 * time.Millisecond,
				P90Time:     10 * time.Millisecond,
				P99Time:     10 * time.Millisecond,
				StdDev:      0.0,
				Invocations: 1,
				NestedCalls: []*profiler.CallMetrics{
					{
						FnName:      "foo",
						TotalTime:   10 * time.Millisecond,
						MeanTime:    5 * time.Millisecond,
						MinTime:     4 * time.Millisecond,
						MaxTime:     6 * time.Millisecond,
						MedianTime:  5 * time.Millisecond,
						P50Time:     4 * time.Millisecond,
						P75Time:     4 * time.Millisecond,
						P90Time:     4 * time.Millisecond,
						P99Time:     6 * time.Millisecond,
						StdDev:      1.41421,
						Invocations: 2,
					},
				},
			},
		},
	}

	var err error
	profileDir, err = ioutil.TempDir("", "prism-test")
	if err != nil {
		t.Fatal(err)
	}

	profileFiles = make([]string, 0)
	for index, pe := range profiles {
		data, err := json.Marshal(pe)
		if err != nil {
			os.RemoveAll(profileDir)
			t.Fatal(err)
		}

		file := fmt.Sprintf("%s/profile-%d.json", profileDir, index)
		err = ioutil.WriteFile(file, data, os.ModePerm)
		if err != nil {
			os.RemoveAll(profileDir)
			t.Fatal(err)
		}
		profileFiles = append(profileFiles, file)
	}

	return profileDir, profileFiles
}
