package cmd

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/geckoboard/prism/profiler"
	"github.com/geckoboard/prism/util"
	"github.com/urfave/cli"
)

const (
	diffEpsilon = 0.01
)

var (
	errNotEnoughProfiles      = errors.New(`"diff" requires at least 2 profiles`)
	errNoDiffColumnsSpecified = errors.New("no table columns specified for diff output")
)

type idToEntryMap map[int]*profiler.Entry
type correlatedEntriesMap map[string]idToEntryMap

// Pretty print a n-way diff between two or more profiles.
func DiffProfiles(ctx *cli.Context) error {
	var err error

	args := ctx.Args()
	if len(args) < 2 {
		return errNotEnoughProfiles
	}

	diffCols, err := util.ParseTableColumList(ctx.String("columns"))
	if err != nil {
		return err
	}
	if len(diffCols) == 0 {
		return errNoDiffColumnsSpecified
	}

	threshold := ctx.Float64("threshold")

	profiles := make([]*profiler.Entry, len(args))
	for index, arg := range args {
		profiles[index], err = profiler.LoadProfile(arg)
		if err != nil {
			return err
		}
	}

	// Correlate profile nodes, build diff payload and tabularize it
	correlMap := correlateEntries(profiles)
	diffTable := tabularizeDiff(profiles, correlMap, diffCols, threshold)

	// If stdout is not a terminal we need to strip ANSI characters
	stripAnsiChars := !terminal.IsTerminal(int(os.Stdout.Fd()))
	diffTable.Write(os.Stdout, stripAnsiChars)

	return nil
}

// Process each profile and return back a map which groups by function name
// profile entries between all profiles.
func correlateEntries(profiles []*profiler.Entry) correlatedEntriesMap {
	// Traverse profile trees and group all entries by name
	entryGroupsByName := make(correlatedEntriesMap, 0)
	for profileId, profile := range profiles {
		populateEntryGroups(profileId, profile, entryGroupsByName)
	}

	return entryGroupsByName
}

// Traverse profile entries and group them together with other profile entries
// that share the same entry name.
func populateEntryGroups(profileId int, pe *profiler.Entry, entryGroupsByName correlatedEntriesMap) {
	if list, exists := entryGroupsByName[pe.Name]; exists {
		// We are working on a copy of the map struct so we need to
		// update its parent with the updated list contents
		list[profileId] = pe
		entryGroupsByName[pe.Name] = list
	} else {
		entryGroupsByName[pe.Name] = idToEntryMap{
			profileId: pe,
		}
	}

	for _, child := range pe.Children {
		populateEntryGroups(profileId, child, entryGroupsByName)
	}
}

// Generate a table with that summarizes all profiles and includes a speedup
// factor for each profile compared to the first (baseline) profile.
func tabularizeDiff(profiles []*profiler.Entry, entryGroupsByName correlatedEntriesMap, diffCols []util.TableColumnType, threshold float64) *util.Table {
	t := &util.Table{
		Headers:      make([]string, len(profiles)*len(diffCols)+1),
		HeaderGroups: make([]util.TableHeaderGroup, len(profiles)+1),
		Rows:         make([][]string, 0),
		Padding:      1,
	}

	// Populate headers
	t.Alignment = make([]util.Alignment, len(t.Headers))

	t.Alignment[0] = util.AlignLeft
	t.Headers[0] = "call stack"
	t.HeaderGroups[0].ColSpan = 1

	startOffset := 1
	for index, _ := range profiles {
		baseIndex := startOffset + index*len(diffCols)

		if index == 0 {
			t.HeaderGroups[startOffset+index].Header = "baseline"
		} else {
			t.HeaderGroups[startOffset+index].Header = fmt.Sprintf("profile %d", index)
		}
		t.HeaderGroups[startOffset+index].ColSpan = len(diffCols)

		for dIndex, dType := range diffCols {
			t.Alignment[baseIndex+dIndex] = util.AlignRight
			t.Headers[baseIndex+dIndex] = dType.Header()
		}
	}

	// Populate rows using first profile
	populateDiffRows(profiles[0], len(profiles), entryGroupsByName, t, diffCols, threshold)

	return t
}

// Populate table rows with profile entry metrics and comparison data.
func populateDiffRows(pe *profiler.Entry, numProfiles int, entryGroupsByName correlatedEntriesMap, t *util.Table, diffCols []util.TableColumnType, threshold float64) {
	row := make([]string, numProfiles*len(diffCols)+1)

	// Fill in call
	call := strings.Repeat("| ", pe.Depth)
	if len(pe.Children) == 0 {
		call += "- "
	} else {
		call += "+ "
	}
	row[0] = call + pe.Name

	baseLine := entryGroupsByName[pe.Name][0]

	// Populate measurement columns
	for profileId, entry := range entryGroupsByName[pe.Name] {
		totalTime := float64(entry.TotalTime.Nanoseconds()) / 1.0e6
		avgTime := float64(entry.TotalTime.Nanoseconds()) / float64(entry.Invocations*1e6)
		minTime := float64(entry.MinTime.Nanoseconds()) / 1.0e6
		maxTime := float64(entry.MaxTime.Nanoseconds()) / 1.0e6

		baseIndex := profileId*len(diffCols) + 1
		if baseLine != nil && profileId != 0 {

			baseTotalTime := float64(baseLine.TotalTime.Nanoseconds()) / 1.0e6
			baseAvgTime := float64(baseLine.TotalTime.Nanoseconds()) / float64(baseLine.Invocations*1e6)
			baseMinTime := float64(baseLine.MinTime.Nanoseconds()) / 1.0e6
			baseMaxTime := float64(baseLine.MaxTime.Nanoseconds()) / 1.0e6

			for dIndex, dType := range diffCols {
				switch dType {
				case util.TableColTotal:
					row[baseIndex+dIndex] = fmtDiff(baseTotalTime, totalTime, threshold)
				case util.TableColAvg:
					row[baseIndex+dIndex] = fmtDiff(baseAvgTime, avgTime, threshold)
				case util.TableColMin:
					row[baseIndex+dIndex] = fmtDiff(baseMinTime, minTime, threshold)
				case util.TableColMax:
					row[baseIndex+dIndex] = fmtDiff(baseMaxTime, maxTime, threshold)
				case util.TableColInvocations:
					row[baseIndex+dIndex] = fmt.Sprintf("%d", entry.Invocations)
				}
			}
		} else {
			for dIndex, dType := range diffCols {
				switch dType {
				case util.TableColTotal:
					row[baseIndex+dIndex] = fmt.Sprintf("%1.2f (---)", totalTime)
				case util.TableColAvg:
					row[baseIndex+dIndex] = fmt.Sprintf("%1.2f (---)", avgTime)
				case util.TableColMin:
					row[baseIndex+dIndex] = fmt.Sprintf("%1.2f (---)", minTime)
				case util.TableColMax:
					row[baseIndex+dIndex] = fmt.Sprintf("%1.2f (---)", maxTime)
				case util.TableColInvocations:
					row[baseIndex+dIndex] = fmt.Sprintf("%d", entry.Invocations)
				}
			}
		}
	}

	// Append row to table
	t.Rows = append(t.Rows, row)

	//  Process children
	for _, child := range pe.Children {
		populateDiffRows(child, numProfiles, entryGroupsByName, t, diffCols, threshold)
	}
}

// Colorize and format candidate including a comparison to the baseline value.
// This method treats lower values as better. If the abs delta difference
// of the two values is less than the threshold then no comparison will be performed.
func fmtDiff(baseLine, candidate float64, threshold float64) string {
	absDelta := math.Abs(baseLine - candidate)
	if absDelta < threshold {
		return fmt.Sprintf("%1.2f (--)", candidate)
	}

	var speedup float64 = 0.0
	if candidate != 0 {
		speedup = baseLine / candidate
	}

	if absDelta < diffEpsilon {
		speedup = 1.0
	}

	var color string
	if speedup == 0.0 || speedup == 1.0 {
		color = "\033[33m" // yellow
	} else if speedup >= 1.0 {
		color = "\033[32m" // green
	} else {
		color = "\033[31m" // red
	}

	return fmt.Sprintf("%1.2f (%s%2.1fx\033[0m)", candidate, color, speedup)
}
