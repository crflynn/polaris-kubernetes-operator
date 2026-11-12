package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("profile", "cover.out", "Go coverage profile")
	minimum := flag.Float64("minimum", 30, "minimum total statement coverage percentage")
	flag.Parse()

	file, err := os.Open(*profile)
	if err != nil {
		fail("open coverage profile: %v", err)
	}
	defer file.Close()

	covered, total, err := parseProfile(file)
	if err != nil {
		fail("read coverage profile: %v", err)
	}
	if total == 0 {
		fail("coverage profile contains no statements")
	}

	percentage := float64(covered) * 100 / float64(total)
	fmt.Printf("Statement coverage: %.1f%% (%d/%d), minimum: %.1f%%\n", percentage, covered, total, *minimum)
	if percentage < *minimum {
		fail("coverage %.1f%% is below the %.1f%% minimum", percentage, *minimum)
	}
}

func parseProfile(profile *os.File) (covered, total int, err error) {
	scanner := bufio.NewScanner(profile)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if line == 1 && strings.HasPrefix(text, "mode: ") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return 0, 0, fmt.Errorf("line %d: expected file range, statement count, and execution count", line)
		}
		statements, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil || statements < 0 {
			return 0, 0, fmt.Errorf("line %d: invalid statement count %q", line, fields[1])
		}
		executions, parseErr := strconv.ParseUint(fields[2], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("line %d: invalid execution count %q", line, fields[2])
		}
		total += statements
		if executions > 0 {
			covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return covered, total, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "coverage check: "+format+"\n", args...)
	os.Exit(1)
}
